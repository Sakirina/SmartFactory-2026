package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func record(t *testing.T, s *Service, id string, at int64, value any, live bool) model.Observation {
	t.Helper()
	ctx := context.Background()
	batch := store.IngestBatch{MessageID: id, SourceID: "edge-a", Points: []model.Observation{{DeviceID: "device", Key: "temperature", ObservedMS: at, Value: value, Quality: "GOOD"}}}
	if _, e := s.Store.Ingest(ctx, batch); e != nil {
		t.Fatal(e)
	}
	q, e := s.Store.Query(ctx, store.Query{FromMS: at, ToMS: at, RawOnly: true, Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	var p model.Observation
	for _, v := range q.Points {
		if v.MessageID == id {
			p = v
		}
	}
	if live {
		if e = s.Process(ctx, p, false, ""); e != nil {
			t.Fatal(e)
		}
	}
	return p
}
func runBackfill(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	doc, e := s.Store.Get(ctx, "job", "backfill:device")
	if e != nil {
		t.Fatal(e)
	}
	job, e := store.Decode[model.Job](doc)
	if e != nil {
		t.Fatal(e)
	}
	job.Version = doc.Version
	if e = s.Recompute(ctx, job); e != nil {
		t.Fatal(e)
	}
}
func TestReplayUsesMoreThanOneDayAndRestoresLiveCounter(t *testing.T) {
	s := engineFixture(t)
	ctx := context.Background()
	base := time.Now().Add(-72 * time.Hour)
	clock := base
	s.Store.Now = func() time.Time { return clock }
	d := definition()
	d.ID = "counter"
	d.EffectiveMS = base.UnixMilli() - 1
	d.Selector.WindowMS = 0
	d.Nodes[1].Type = "counter"
	d.Nodes[1].Params = map[string]any{"mode": "delta"}
	if _, e := s.Store.Put(ctx, "definition", d.ID, 0, d); e != nil {
		t.Fatal(e)
	}
	record(t, s, "first", base.UnixMilli(), 1, true)
	clock = base.Add(48 * time.Hour)
	record(t, s, "second", clock.UnixMilli(), 2, true)
	// Replay must recover raw observations and their previous derived values
	// after both have moved into compressed storage.
	stats, archiveErr := s.Store.ArchiveObservations(ctx)
	if archiveErr != nil || stats.Points < 2 {
		t.Fatal(stats, archiveErr)
	}
	clock = clock.Add(time.Second)
	record(t, s, "late", base.Add(24*time.Hour).UnixMilli(), 4, false)
	runBackfill(t, s)
	p, e := s.Store.Latest(ctx, "device", "counter.mean")
	if e != nil || fmt.Sprint(p.Value) != "7" {
		t.Fatalf("replay: %+v %v", p, e)
	}
	clock = clock.Add(time.Second)
	record(t, s, "next", clock.UnixMilli(), 1, true)
	p, e = s.Store.Latest(ctx, "device", "counter.mean")
	if e != nil || fmt.Sprint(p.Value) != "8" {
		t.Fatalf("live state: %+v %v", p, e)
	}
	q, e := s.Store.Query(ctx, store.Query{FromMS: base.UnixMilli(), ToMS: clock.UnixMilli(), IncludeRevisions: true, Limit: 100})
	if e != nil || len(q.Revisions) == 0 {
		t.Fatal("no before/after revision", e)
	}
}
func TestHistoricalAlarmKeepsAcknowledgementAndCurrentEpisode(t *testing.T) {
	s := engineFixture(t)
	ctx := context.Background()
	clock := time.Now().Add(-time.Hour)
	start := clock.UnixMilli()
	s.Store.Now = func() time.Time { return clock }
	d := definition()
	d.ID = "alarm"
	d.Kind = "alarm"
	d.EffectiveMS = start - 1
	d.Outputs = nil
	d.Selector.WindowMS = 0
	d.Nodes[1].Type = "hysteresis"
	d.Nodes[1].Params = map[string]any{"high": 30, "low": 27}
	d.Nodes[2].Type = "alarm"
	d.Policy.Channels = []string{"email", "sms"}
	if _, e := s.Store.Put(ctx, "definition", d.ID, 0, d); e != nil {
		t.Fatal(e)
	}
	record(t, s, "high", start, 35, true)
	doc, e := s.Store.Get(ctx, "active_alarm", "alarm:device")
	if e != nil {
		t.Fatal(e)
	}
	first, e := store.Decode[model.Alarm](doc)
	if e != nil {
		t.Fatal(e)
	}
	first.Acknowledged = true
	if _, e = s.Store.Put(ctx, "active_alarm", "alarm:device", doc.Version, first); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.Put(ctx, "alarm", first.ID, -1, first); e != nil {
		t.Fatal(e)
	}
	clock = clock.Add(2 * time.Second)
	record(t, s, "clear", clock.UnixMilli(), 25, true)
	clock = clock.Add(2 * time.Second)
	record(t, s, "new-high", clock.UnixMilli(), 36, true)
	clock = clock.Add(time.Second)
	record(t, s, "late", start+1000, 33, false)
	runBackfill(t, s)
	doc, e = s.Store.Get(ctx, "alarm", first.ID)
	if e != nil {
		t.Fatal(e)
	}
	first, e = store.Decode[model.Alarm](doc)
	if e != nil || first.Active || !first.Acknowledged || !first.Historical {
		t.Fatalf("historical episode: %+v %v", first, e)
	}
	doc, e = s.Store.Get(ctx, "active_alarm", "alarm:device")
	if e != nil {
		t.Fatal(e)
	}
	current, e := store.Decode[model.Alarm](doc)
	if e != nil || !current.Active || current.StartedMS != start+4000 {
		t.Fatalf("current episode: %+v %v", current, e)
	}
	items, e := s.Store.Deliveries(ctx, "notification", 100)
	if e != nil {
		t.Fatal(e)
	}
	historicalNotifications := 0
	for _, item := range items {
		var n struct {
			Alarm model.Alarm `json:"alarm"`
		}
		store.DecodeJSON(item.Payload, &n)
		if n.Alarm.Historical {
			historicalNotifications++
			if item.Destination != "in_app" {
				t.Fatal("ended historical alarm sent external notification")
			}
		}
	}
	if historicalNotifications != 1 {
		t.Fatal("historical notice count", historicalNotifications)
	}
}
func TestExpressionErrorUsesOnlyTheErrorBranch(t *testing.T) {
	s := engineFixture(t)
	d := definition()
	d.Selector.WindowMS = 0
	d.Nodes = []model.Node{{ID: "source", Type: "input"}, {ID: "divide", Type: "expression", Params: map[string]any{"code": "1/0"}}, {ID: "failure", Type: "output"}, {ID: "success", Type: "output"}}
	d.Outputs = nil
	d.Connections = []model.Connection{{From: "source", To: "divide"}, {From: "divide", FromPort: "error", To: "failure"}, {From: "divide", To: "success"}}
	r, e := s.Evaluate(context.Background(), d, model.Observation{Value: 1, Quality: "GOOD", ObservedMS: time.Now().UnixMilli()}, false, nil)
	if e != nil || r.Values["failure"] != "division by zero" {
		t.Fatal(r, e)
	}
	if _, ok := r.Values["success"]; ok {
		t.Fatal("success branch executed after failure")
	}
}

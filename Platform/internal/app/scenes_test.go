package app

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestSceneTimersQualityHysteresisAndRecovery(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "scenes.db"), "edge-a", bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	e := &engine.Service{Store: s, ControlEnabled: true}
	for _, id := range []string{"climate-1", "light-1", "gas-1", "agv-1", "counter-1"} {
		if _, err = s.Put(ctx, "entity", id, 0, model.Entity{ID: id, Kind: "device", EdgeID: "edge-a", Status: "approved", SamplingMS: 1000}); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range ExampleDefinitions() {
		d.SchemaVersion = "1.0"
		d.GroupID = "factory"
		d.Status = "published"
		d.Version = 1
		d.EffectiveMS = now.UnixMilli()
		if v := e.Validate(ctx, d); !v.Valid {
			t.Fatal(d.ID, v.Errors)
		}
		if _, err = s.Put(ctx, "definition", d.ID, 0, d); err != nil {
			t.Fatal(err)
		}
	}
	sequence := 0
	ingest := func(device, key string, value any, quality string) {
		t.Helper()
		sequence++
		p := model.Observation{ID: fmt.Sprintf("input-%d", sequence), DeviceID: device, Key: key, Value: value, Quality: quality, ObservedMS: now.UnixMilli(), SourceID: "edge-a", TimeSource: "device"}
		if _, err = s.Ingest(ctx, store.IngestBatch{MessageID: p.ID, SourceID: "edge-a", PayloadHash: store.Hash(p), Points: []model.Observation{p}}); err != nil {
			t.Fatal(err)
		}
		if err = e.Process(ctx, p, false, ""); err != nil {
			t.Fatal(err)
		}
	}
	count := func(id string) int {
		t.Helper()
		items, err := s.Deliveries(ctx, "strategy_trigger", 1000)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, item := range items {
			var v struct {
				DefinitionID string `json:"definition_id"`
			}
			if err = store.DecodeJSON(item.Payload, &v); err != nil {
				t.Fatal(err)
			}
			if v.DefinitionID == id {
				n++
			}
		}
		return n
	}
	tick := func(after time.Duration) {
		t.Helper()
		now = now.Add(after)
		if err = e.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ingest("light-1", "presence", true, "GOOD")
	tick(50 * time.Millisecond)
	if count("light-on") != 0 {
		t.Fatal("presence bounce lit lamp")
	}
	ingest("light-1", "presence", false, "GOOD")
	tick(200 * time.Millisecond)
	if count("light-on") != 0 {
		t.Fatal("cancelled bounce lit lamp")
	}
	ingest("light-1", "presence", true, "GOOD")
	tick(101 * time.Millisecond)
	if count("light-on") != 1 {
		t.Fatal("timer did not light lamp without another sample")
	}
	tick(100 * time.Millisecond)
	ingest("light-1", "presence", true, "GOOD")
	if count("light-on") != 1 {
		t.Fatal("repeated presence duplicated action")
	}
	ingest("light-1", "presence", false, "GOOD")
	tick(1999 * time.Millisecond)
	if count("light-off") != 0 {
		t.Fatal("light switched off early")
	}
	tick(2 * time.Millisecond)
	if count("light-off") != 1 {
		t.Fatal("light did not switch off after two seconds")
	}
	// A short visit must reset the earlier absence episode immediately, even
	// though the next absence still needs two seconds before switching off.
	ingest("light-1", "presence", true, "GOOD")
	tick(101 * time.Millisecond)
	if count("light-on") != 2 {
		t.Fatal("second visit did not light lamp")
	}
	ingest("light-1", "presence", false, "GOOD")
	tick(2001 * time.Millisecond)
	if count("light-off") != 2 {
		t.Fatal("short visit prevented the next delayed switch-off")
	}
	ingest("agv-1", "distance", 150, "GOOD")
	tick(5001 * time.Millisecond)
	if count("agv-stop") != 1 {
		t.Fatal("stale distance did not stop AGV")
	}
	tick(time.Second)
	if count("agv-stop") != 1 {
		t.Fatal("watchdog duplicated action")
	}
	ingest("agv-1", "distance", 150, "GOOD")
	ingest("agv-1", "distance", 150, "BAD")
	if count("agv-stop") != 2 {
		t.Fatal("invalid distance did not stop AGV")
	}
	for _, p := range []struct {
		field       string
		on, restore float64
		alarm       string
	}{{"temperature", 17, 21, "climate-low"}, {"humidity", 75, 60, "humidity-high"}, {"humidity", 25, 40, "humidity-low"}, {"smoke", 6, 1, "gas-smoke-alarm"}, {"combustible", 25, 5, "gas-combustible-alarm"}, {"co", 35, 5, "gas-co-alarm"}} {
		device := "climate-1"
		if p.field == "smoke" || p.field == "combustible" || p.field == "co" {
			device = "gas-1"
		}
		now = now.Add(time.Second)
		ingest(device, p.field, p.on, "GOOD")
		doc, err := s.Get(ctx, "active_alarm", p.alarm+":"+device)
		if err != nil {
			t.Fatal(p.alarm, err)
		}
		alarm, _ := store.Decode[model.Alarm](doc)
		if !alarm.Active {
			t.Fatal(p.alarm, "did not activate")
		}
		now = now.Add(time.Second)
		ingest(device, p.field, p.restore, "GOOD")
		doc, err = s.Get(ctx, "active_alarm", p.alarm+":"+device)
		if err != nil {
			t.Fatal(err)
		}
		alarm, _ = store.Decode[model.Alarm](doc)
		if alarm.Active {
			t.Fatal(p.alarm, "did not recover")
		}
	}
}

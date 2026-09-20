package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func engineFixture(t *testing.T) *Service {
	t.Helper()
	s, e := store.Open(context.Background(), filepath.Join(t.TempDir(), "engine.db"), "edge-a", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return &Service{Store: s}
}
func definition() model.Definition {
	return model.Definition{ID: "mean", Name: "Temperature mean", Kind: "analysis", SchemaVersion: "1.0", Status: "published", Version: 1, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"temperature"}, WindowMS: 60000}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "sum", Type: "aggregate", Params: map[string]any{"function": "avg"}}, {ID: "result", Type: "output"}}, Connections: []model.Connection{{From: "input", To: "sum"}, {From: "sum", To: "result"}}, Outputs: []model.Output{{Key: "mean", Type: "number", NodeID: "result", Unit: "°C"}}}
}
func TestExpressionsResourceBudgetAndPrecision(t *testing.T) {
	ctx := context.Background()
	for code, want := range map[string]string{"value+1": "9007199254740994", "choose(value>0, max(1,2), 0)": "2", "abs(-2)+round(2.4)": "4"} {
		v, e := Expression(ctx, code, map[string]any{"value": json.Number("9007199254740993")})
		if e != nil || fmt.Sprint(v) != want {
			t.Fatalf("%s: %v %v", code, v, e)
		}
	}
	for _, code := range []string{"os.Exit(0)", "value[0]", "func() { for {} }()", "unknown(2)", "1/0"} {
		if _, e := Expression(ctx, code, map[string]any{"value": 1}); e == nil {
			t.Fatalf("unsafe/invalid expression accepted: %s", code)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := Expression(cancelled, "1+1", nil); e == nil {
		t.Fatal("cancelled evaluation proceeded")
	}
}
func TestTypedGraphAndDerivedRevision(t *testing.T) {
	s := engineFixture(t)
	ctx := context.Background()
	d := definition()
	v := s.Validate(ctx, d)
	if !v.Valid {
		t.Fatal(v.Errors)
	}
	cyclic := definition()
	cyclic.Connections = append(cyclic.Connections, model.Connection{From: "result", To: "input"})
	if s.Validate(ctx, cyclic).Valid {
		t.Fatal("cycle accepted")
	}
	bad := definition()
	bad.Nodes[0].Outputs = []model.Port{{Name: "value", Type: "boolean"}}
	bad.Nodes[1].Inputs = []model.Port{{Name: "value", Type: "number"}}
	bad.Connections[0].FromPort = "value"
	bad.Connections[0].ToPort = "value"
	if s.Validate(ctx, bad).Valid {
		t.Fatal("port mismatch accepted")
	}
	now := time.Now().UnixMilli()
	d.EffectiveMS = now - 10000
	if _, e := s.Store.Put(ctx, "definition", d.ID, 0, d); e != nil {
		t.Fatal(e)
	}
	for i, point := range []struct {
		offset  int64
		value   int
		quality string
	}{{-2000, 10, "GOOD"}, {0, 30, "GOOD"}, {-1000, 999, "BAD"}} {
		batch := store.IngestBatch{MessageID: fmt.Sprint(i), SourceID: "edge-a", Points: []model.Observation{{DeviceID: "device", Key: "temperature", ObservedMS: now + point.offset, Value: point.value, Quality: point.quality}}}
		if _, e := s.Store.Ingest(ctx, batch); e != nil {
			t.Fatal(e)
		}
	}
	p := model.Observation{ID: "evaluate-1", DeviceID: "device", Key: "temperature", ObservedMS: now, ReceivedMS: now, Value: 30, Quality: "GOOD"}
	if e := s.Process(ctx, p, false, ""); e != nil {
		t.Fatal(e)
	}
	q, e := s.Store.Query(ctx, store.Query{DeviceIDs: []string{"device"}, Keys: []string{"mean.mean"}, FromMS: now, ToMS: now, Limit: 10})
	if e != nil || len(q.Points) != 1 || fmt.Sprint(q.Points[0].Value) != "20" {
		t.Fatalf("derived result: %+v %v", q, e)
	}
	if _, e := s.Store.Ingest(ctx, store.IngestBatch{MessageID: "late", SourceID: "edge-a", Points: []model.Observation{{DeviceID: "device", Key: "temperature", ObservedMS: now - 500, Value: 80, Quality: "GOOD"}}}); e != nil {
		t.Fatal(e)
	}
	if e = s.Process(ctx, p, true, "job-1"); e != nil {
		t.Fatal(e)
	}
	q, e = s.Store.Query(ctx, store.Query{DeviceIDs: []string{"device"}, Keys: []string{"mean.mean"}, FromMS: now, ToMS: now, Limit: 10, IncludeRevisions: true})
	if e != nil || len(q.Points) != 1 || fmt.Sprint(q.Points[0].Value) != "40" || len(q.Revisions) != 1 {
		t.Fatalf("corrected result: %+v %v", q, e)
	}
}
func TestCounterSurvivesDuplicateAndRestart(t *testing.T) {
	s := engineFixture(t)
	ctx := context.Background()
	d := definition()
	d.ID = "counter"
	d.Selector.WindowMS = 0
	d.Nodes[1].Type = "counter"
	d.Nodes[1].Params = map[string]any{"mode": "delta"}
	if _, e := s.Store.Put(ctx, "definition", d.ID, 0, d); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UnixMilli()
	for i := 0; i < 100; i++ {
		p := model.Observation{ID: fmt.Sprint(i), DeviceID: "device", Key: "temperature", ObservedMS: now + int64(i), Value: 1, Quality: "GOOD"}
		if e := s.Process(ctx, p, false, ""); e != nil {
			t.Fatal(e)
		}
		if e := s.Process(ctx, p, false, ""); e != nil {
			t.Fatal(e)
		}
		if i == 50 {
			s = &Service{Store: s.Store}
		}
	}
	p, e := s.Store.Latest(ctx, "device", "counter.mean")
	if e != nil || fmt.Sprint(p.Value) != "100" {
		t.Fatalf("counter %+v %v", p, e)
	}
}

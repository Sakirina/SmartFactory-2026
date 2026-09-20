package engine

import (
	"context"
	"testing"
	"time"

	"competition2026/product/platform/pkg/model"
)

func TestStrategyContinuesWhileHistoricalMergeOwnsCalculationLock(t *testing.T) {
	s := engineFixture(t)
	s.ControlEnabled = true
	ctx := context.Background()
	d := model.Definition{ID: "ventilate", Name: "Ventilate", Kind: "strategy", SchemaVersion: "1.0", Status: "published", Version: 1, GroupID: "factory",
		Selector:    model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"temperature"}},
		Nodes:       []model.Node{{ID: "input", Type: "input"}, {ID: "condition", Type: "threshold", Params: map[string]any{"operator": ">", "value": 30}}, {ID: "action", Type: "action"}},
		Connections: []model.Connection{{From: "input", To: "condition"}, {From: "condition", To: "action"}},
		Policy:      model.Policy{EdgeIDs: []string{"edge-a"}, Steps: []model.Step{{ID: "fan", DeviceID: "device", EdgeID: "edge-a", Action: "set_fan", Idempotent: true}}}}
	if _, err := s.Store.Put(ctx, "definition", d.ID, 0, d); err != nil {
		t.Fatal(err)
	}
	p := record(t, s, "fresh-control", time.Now().UnixMilli(), 35, false)
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- s.ProcessStrategies(ctx, p) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("historical calculation prevented a fresh control decision")
	}
	items, err := s.Store.Deliveries(ctx, "strategy_trigger", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("control request was not committed: %d %v", len(items), err)
	}
	if err = s.ProcessStrategies(ctx, p); err != nil {
		t.Fatal(err)
	}
	items, err = s.Store.Deliveries(ctx, "strategy_trigger", 10)
	if err != nil || len(items) != 1 {
		t.Fatal("duplicate strategy delivery created another control request")
	}
}

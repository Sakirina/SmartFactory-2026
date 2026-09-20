package engine

import (
	"competition2026/product/platform/internal/store"
	"context"
	"fmt"
	"testing"
	"time"
)

func TestQueuedRealtimeObservationCreatesRecomputationBeforeAcknowledgement(t *testing.T) {
	s := engineFixture(t)
	ctx := context.Background()
	clock := time.Now()
	s.Store.Now = func() time.Time { return clock }
	d := definition()
	d.ID = "counter"
	d.EffectiveMS = clock.UnixMilli() - 1
	d.Selector.WindowMS = 0
	d.Nodes[1].Type = "counter"
	d.Nodes[1].Params = map[string]any{"mode": "delta"}
	if _, e := s.Store.Put(ctx, "definition", d.ID, 0, d); e != nil {
		t.Fatal(e)
	}
	p := record(t, s, "queued", clock.UnixMilli(), 7, false)
	if p.Late {
		t.Fatal("fixture ingress should initially be timely")
	}
	clock = clock.Add(time.Minute)
	if e := s.Process(ctx, p, true, ""); e != nil {
		t.Fatal(e)
	}
	job, e := s.Store.Get(ctx, "job", "backfill:device")
	if e != nil {
		t.Fatal("lost deferred calculation", e)
	}
	if e = s.Process(ctx, p, true, ""); e != nil {
		t.Fatal(e)
	}
	again, _ := s.Store.Get(ctx, "job", job.ID)
	if again.Version != job.Version {
		t.Fatal("repeated callback duplicated job")
	}
	runBackfill(t, s)
	output, e := s.Store.Latest(ctx, "device", "counter.mean")
	if e != nil || fmt.Sprint(output.Value) != "7" {
		t.Fatal(output, e)
	}
	completed, _ := s.Store.Get(ctx, "job", job.ID)
	if e = s.Process(ctx, output, true, ""); e != nil {
		t.Fatal(e)
	}
	after, _ := s.Store.Get(ctx, "job", job.ID)
	if after.Version != completed.Version {
		t.Fatal("historical output created endless recomputation")
	}
	if _, e = store.Decode[map[string]any](completed); e != nil {
		t.Fatal(e)
	}
}

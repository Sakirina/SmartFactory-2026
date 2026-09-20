package cloudsync

import (
	"competition2026/product/platform/internal/store"
	"context"
	"testing"
	"time"
)

func TestIdleExchangesDoNotWriteUntilHeartbeat(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	now := time.Now()
	f.cloud.Now = func() time.Time { return now }
	f.edge.Now = func() time.Time { return now }
	for range 4 {
		if err := f.client.Exchange(ctx); err != nil {
			t.Fatal(err)
		}
	}
	changes := func(s *store.Store) int64 {
		var n int64
		if err := s.DB.QueryRow("SELECT total_changes()").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	cloudBefore, edgeBefore := changes(f.cloud), changes(f.edge)
	now = now.Add(4 * time.Second)
	for range 25 {
		if err := f.client.Exchange(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := changes(f.cloud) - cloudBefore; n != 0 {
		t.Fatalf("idle cloud changed %d rows", n)
	}
	if n := changes(f.edge) - edgeBefore; n != 0 {
		t.Fatalf("idle edge changed %d rows", n)
	}
	now = now.Add(time.Second)
	if err := f.client.Exchange(ctx); err != nil {
		t.Fatal(err)
	}
	if n := changes(f.cloud) - cloudBefore; n != 1 {
		t.Fatalf("heartbeat changed %d rows, want one", n)
	}
	if n := changes(f.edge) - edgeBefore; n != 0 {
		t.Fatalf("heartbeat wrote unchanged edge cursor: %d", n)
	}
	now = now.Add(16 * time.Second)
	sources, err := f.cloud.Sources(ctx)
	if err != nil || len(sources) != 1 || sources[0].Status != "offline" {
		t.Fatal(sources, err)
	}
}

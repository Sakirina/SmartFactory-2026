package store

import (
	"context"
	"testing"
	"time"
)

func TestTelemetryDedupExpiresWhileBusinessIdentityRemains(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := s.Now()
	telemetry := sample(s, "telemetry", 1)
	telemetry.Critical = false
	critical := sample(s, "business-event", 1)
	if _, err := s.IngestMessages(ctx, []IngestBatch{telemetry, critical}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(29 * 24 * time.Hour)
	s.Now = func() time.Time { return now }
	if _, err := s.ApplyRetention(ctx, DefaultRetention()); err != nil {
		t.Fatal(err)
	}
	if result, err := s.Ingest(ctx, telemetry); err != nil || !result.Duplicate {
		t.Fatal("30-day dedup lost", result, err)
	}
	now = now.Add(2 * 24 * time.Hour)
	if _, err := s.ApplyRetention(ctx, DefaultRetention()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM inbox WHERE id='telemetry'").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if result, err := s.Ingest(ctx, critical); err != nil || !result.Duplicate {
		t.Fatal("business identity expired", result, err)
	}
	if result, err := s.Ingest(ctx, telemetry); err != nil || result.Count != 0 {
		t.Fatal("expired data entered automatic computation", result, err)
	}
	if _, err := s.Get(ctx, "quarantine", "telemetry:0"); err != nil {
		t.Fatal("expired data missing from manual review", err)
	}
}

package store

import (
	"context"
	"testing"
	"time"

	"competition2026/product/platform/pkg/model"
)

func bulkTaskBudget(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	now := s.Now().UnixMilli()
	points := make([]model.Observation, 10000)
	for index := range points {
		points[index] = model.Observation{DeviceID: "bulk", Key: "value", ObservedMS: now + int64(index), Quality: "GOOD", Value: index}
	}
	batch := IngestBatch{MessageID: "bulk-tasks", SourceID: "collector", Points: points}
	started := time.Now()
	result, err := s.Ingest(ctx, batch)
	elapsed := time.Since(started)
	if err != nil || !result.Committed || elapsed > 30*time.Second {
		t.Fatal(result, elapsed, err)
	}
	for table, want := range map[string]int{"observations": 10000, "outbox": 30000, "sf_tasks": 10000, "river_job": 10000} {
		var count int
		if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Fatal(table, count, err)
		}
	}
	if _, err = s.Ingest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM river_job").Scan(&count); err != nil || count != 10000 {
		t.Fatal(count, err)
	}
	t.Logf("%s bulk ingest: 10000 observations +30000 outbox +10000 official River jobs in %s; repeated message kept exactly10000 tasks", s.Driver, elapsed)
}
func TestSQLiteBulkIngestTasksBudget(t *testing.T)   { bulkTaskBudget(t, testStore(t)) }
func TestPostgresBulkIngestTasksBudget(t *testing.T) { bulkTaskBudget(t, pgStore(t)) }

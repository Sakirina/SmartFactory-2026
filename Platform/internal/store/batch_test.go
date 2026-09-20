package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"competition2026/product/platform/pkg/model"
)

func TestBatchIngestPreservesOrderDeduplicationAndAtomicFailure(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := s.Now().UnixMilli()
	batch := IngestBatch{MessageID: "batch", SourceID: "edge", Points: []model.Observation{}}
	for i := 0; i < 600; i++ {
		batch.Points = append(batch.Points, model.Observation{DeviceID: fmt.Sprintf("device-%03d", i), Key: "value", Value: i, Quality: "GOOD", ObservedMS: at})
	}
	batch.Points = append(batch.Points,
		model.Observation{DeviceID: "device-000", Key: "value", Value: 1000, Quality: "GOOD", ObservedMS: at + 1},
		model.Observation{DeviceID: "device-000", Key: "value", Value: 999, Quality: "GOOD", ObservedMS: at - 1},
		model.Observation{DeviceID: "device-000", Key: "value", Value: 1001, Quality: "GOOD", ObservedMS: at + 1})
	// Fail after the first insert chunk. No earlier observations, latest values,
	// inbox acknowledgement or delivery records may escape the transaction.
	if _, e := s.DB.ExecContext(ctx, `CREATE TRIGGER reject_batch BEFORE INSERT ON observations WHEN NEW.device_id='device-500' BEGIN SELECT RAISE(ABORT,'injected disk failure'); END`); e != nil {
		t.Fatal(e)
	}
	result, e := s.Ingest(ctx, batch)
	if e == nil || result.Committed {
		t.Fatal("failed transaction acknowledged", result, e)
	}
	for _, table := range []string{"observations", "latest", "inbox", "outbox"} {
		var count int
		if e = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); e != nil || count != 0 {
			t.Fatal(table, count, e)
		}
	}
	if _, e = s.DB.ExecContext(ctx, "DROP TRIGGER reject_batch"); e != nil {
		t.Fatal(e)
	}
	result, e = s.Ingest(ctx, batch)
	if e != nil || !result.Committed || result.Count != 603 || result.Late != 1 {
		t.Fatal(result, e)
	}
	last, e := s.Latest(ctx, "device-000", "value")
	if e != nil || fmt.Sprint(last.Value) != "1001" || last.ObservedMS != at+1 {
		t.Fatal(last, e)
	}
	result, e = s.Ingest(ctx, batch)
	if e != nil || !result.Duplicate || !result.Committed {
		t.Fatal(result, e)
	}
	for table, want := range map[string]int{"observations": 603, "latest": 600, "inbox": 1, "outbox": 1808} {
		var count int
		if e = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); e != nil || count != want {
			t.Fatal(table, count, e)
		}
	}
	batch.Points[0].Value = 1234
	if _, e = s.Ingest(ctx, batch); !errors.Is(e, ErrConflict) {
		t.Fatal("changed duplicate accepted", e)
	}
}

func TestLateMessagesShareOneBackfillRevisionPerDeviceAndCommit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := s.Now().UnixMilli()
	batches := []IngestBatch{}
	for i := 0; i < 100; i++ {
		batches = append(batches, IngestBatch{MessageID: fmt.Sprintf("late-%d", i), SourceID: "edge", Points: []model.Observation{
			{DeviceID: "sensor", Key: "temperature", ObservedMS: now - 60000 + int64(i), Quality: "GOOD", Value: i},
			{DeviceID: "sensor", Key: "humidity", ObservedMS: now - 60000 + int64(i), Quality: "GOOD", Value: i},
		}})
	}
	results, err := s.IngestMessages(ctx, batches)
	if err != nil || len(results) != 100 {
		t.Fatal(results, err)
	}
	doc, err := s.Get(ctx, "job", "backfill:sensor")
	if err != nil || doc.Version != 1 {
		t.Fatal(doc, err)
	}
	job, err := Decode[model.Job](doc)
	if err != nil || job.FromMS != now-60000 || job.ToMS != now-60000+99 {
		t.Fatal(job, err)
	}
	if _, err = s.IngestMessages(ctx, batches); err != nil {
		t.Fatal(err)
	}
	doc, err = s.Get(ctx, "job", "backfill:sensor")
	if err != nil || doc.Version != 1 {
		t.Fatal("duplicates changed the job", doc, err)
	}
	job.Status = "running"
	if _, err = s.Put(ctx, "job", job.ID, doc.Version, job); err != nil {
		t.Fatal(err)
	}
	batches[0].MessageID = "late-after-replay-start"
	batches[0].Points[0].ObservedMS = now - 70000
	if _, err = s.Ingest(ctx, batches[0]); err != nil {
		t.Fatal(err)
	}
	doc, err = s.Get(ctx, "job", "backfill:sensor")
	job, decodeErr := Decode[model.Job](doc)
	if err != nil || decodeErr != nil || doc.Version != 3 || job.Status != "pending" || job.FromMS != now-70000 || job.ToMS != now-60000+99 {
		t.Fatal(job, doc, err, decodeErr)
	}
}

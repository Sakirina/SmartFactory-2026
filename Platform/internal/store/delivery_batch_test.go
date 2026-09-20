package store

import (
	"context"
	"errors"
	"testing"
)

func TestDeliveryBatchCommitFailurePreservesAllResults(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *Tx) error {
		for _, id := range []string{"a", "b", "c"} {
			if err := tx.Enqueue(id, "test", "device", id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`CREATE TRIGGER fail_completion BEFORE UPDATE ON outbox WHEN NEW.id='c' BEGIN SELECT RAISE(ABORT,'write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDeliveries(ctx, []string{"a", "b"}, map[string]error{"c": errors.New("retry")}); err == nil {
		t.Fatal("failed batch was acknowledged")
	}
	items, err := s.Deliveries(ctx, "test", 10)
	if err != nil || len(items) != 3 {
		t.Fatal("partial batch committed", items, err)
	}
	if _, err = s.DB.Exec("DROP TRIGGER fail_completion"); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteDeliveries(ctx, []string{"a", "b"}, map[string]error{"c": errors.New("retry")}); err != nil {
		t.Fatal(err)
	}
	var count, attempts int
	if err = s.DB.QueryRow("SELECT COUNT(*),SUM(attempts) FROM outbox").Scan(&count, &attempts); err != nil || count != 1 || attempts != 1 {
		t.Fatal(count, attempts, err)
	}
}

func TestMultiMessageIngestFailureDoesNotAcknowledgeEarlierMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	batch := []IngestBatch{sample(s, "one", 1), sample(s, "two", 2)}
	batch[1].Points[0].Quality = "invalid"
	results, err := s.IngestMessages(ctx, batch)
	if err == nil {
		t.Fatal("invalid message committed")
	}
	for _, result := range results {
		if result.Committed {
			t.Fatal("failed batch acknowledged", results)
		}
	}
	var count int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM inbox").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	batch[1].Points[0].Quality = "GOOD"
	results, err = s.IngestMessages(ctx, batch)
	if err != nil || len(results) != 2 || !results[0].Committed || !results[1].Committed {
		t.Fatal(results, err)
	}
	results, err = s.IngestMessages(ctx, batch)
	if err != nil || !results[0].Duplicate || !results[1].Duplicate {
		t.Fatal(results, err)
	}
}

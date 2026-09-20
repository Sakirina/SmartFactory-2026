package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/pkg/model"
)

func testStore(t *testing.T) *Store { return testStoreAt(t, ":memory:") }
func testStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	s, e := Open(context.Background(), path, "edge-a", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 9, 20, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	return s
}
func sample(s *Store, id string, value any) IngestBatch {
	return IngestBatch{MessageID: id, SourceID: "edge-a", Critical: true, Event: map[string]any{"kind": "count"}, Points: []model.Observation{{DeviceID: "counter-1", Key: "count", Value: value, ObservedMS: s.Now().UnixMilli(), Quality: "GOOD", TimeSource: "device"}}}
}
func TestDurableInboxConflictRollbackAndRestart(t *testing.T) {
	ctx := context.Background()
	s := testStoreAt(t, filepath.Join(t.TempDir(), "test.db"))
	p := sample(s, "event-1", json.Number("9007199254740993"))
	r, e := s.Ingest(ctx, p)
	if e != nil || !r.Committed || r.Duplicate {
		t.Fatalf("initial: %+v %v", r, e)
	}
	r, e = s.Ingest(ctx, p)
	if e != nil || !r.Duplicate || r.Count != 0 {
		t.Fatalf("repeat: %+v %v", r, e)
	}
	p.Points[0].Value = 7
	if _, e = s.Ingest(ctx, p); !errors.Is(e, ErrConflict) {
		t.Fatalf("conflicting ID: %v", e)
	}
	p = sample(s, "event-2", 3)
	p.Points = append(p.Points, model.Observation{DeviceID: "counter-1", ObservedMS: s.Now().UnixMilli(), Quality: "GOOD"})
	r, e = s.Ingest(ctx, p)
	if e == nil || r.Committed {
		t.Fatal("invalid transaction acknowledged")
	}
	if _, e = s.Get(ctx, "event", "event-2"); !errors.Is(e, ErrNotFound) {
		t.Fatal("transaction was not rolled back", e)
	}
	value, e := s.Latest(ctx, "counter-1", "count")
	if e != nil || fmt.Sprint(value.Value) != "9007199254740993" {
		t.Fatalf("integer precision: %+v %v", value, e)
	}
	r2, e := s.Query(ctx, Query{DeviceIDs: []string{"counter-1"}, Limit: 10})
	if e != nil || len(r2.Points) != 1 || fmt.Sprint(r2.Points[0].Value) != "9007199254740993" {
		t.Fatalf("query: %+v %v", r2, e)
	}
	var path string
	if e = s.DB.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); e != nil {
		t.Fatal(e)
	}
	s.Close()
	reopened, e := Open(ctx, path, "edge-a", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	reopened.Now = s.Now
	r, e = reopened.Ingest(ctx, sample(reopened, "event-1", json.Number("9007199254740993")))
	if e != nil || !r.Duplicate {
		t.Fatalf("restart: %+v %v", r, e)
	}
	reopened.Close()
	r, e = reopened.Ingest(ctx, sample(reopened, "event-3", 1))
	if e == nil || r.Committed {
		t.Fatal("closed database acknowledged a message")
	}
}
func TestAuditDetectsModificationDeletionInsertionAndReorder(t *testing.T) {
	for _, mutation := range []string{"UPDATE audit SET data=replace(data,'change','tamper') WHERE sequence=2", "DELETE FROM audit WHERE sequence=2", "UPDATE audit SET sequence=9 WHERE sequence=2", "UPDATE audit SET previous_hash='altered' WHERE sequence=2", "DELETE FROM audit WHERE sequence=3"} {
		t.Run(mutation, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			for i := 0; i < 3; i++ {
				if e := s.Audit(ctx, model.Actor{UserID: "engineer"}, "change", "asset", fmt.Sprint(i), map[string]any{"value": i}); e != nil {
					t.Fatal(e)
				}
			}
			issues, e := s.VerifyAudit(ctx)
			if e != nil || len(issues) != 0 {
				t.Fatalf("clean chain: %+v %v", issues, e)
			}
			if _, e = s.DB.Exec(mutation); e != nil {
				t.Fatal(e)
			}
			issues, e = s.VerifyAudit(ctx)
			if e != nil || len(issues) == 0 {
				t.Fatalf("tampering not detected: %+v %v", issues, e)
			}
		})
	}
}
func TestAuditPreservesStructFieldOrderAndExactNumbers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	snapshot := struct {
		Z string `json:"z"`
		A any    `json:"a"`
	}{"struct fields differ from sorted map keys", json.Number("9007199254740993")}
	if err := s.Audit(ctx, model.Actor{UserID: "engineer"}, "create", "device", "request-1", snapshot); err != nil {
		t.Fatal(err)
	}
	issues, err := s.VerifyAudit(ctx)
	if err != nil || len(issues) != 0 {
		t.Fatalf("stored bytes must remain verifiable: %+v %v", issues, err)
	}
}
func TestLateQualityRollupAndRetention(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := s.Now().UnixMilli()
	for i, value := range []int64{10, 20, 30} {
		p := sample(s, fmt.Sprint(i), value)
		p.Critical = false
		p.Points[0].ObservedMS = now - int64(i)*1000
		if i == 1 {
			p.Points[0].Quality = "BAD"
			p.Points[0].QualityReason = "protocol error"
		}
		if _, e := s.Ingest(ctx, p); e != nil {
			t.Fatal(e)
		}
	}
	job, e := s.Get(ctx, "job", "backfill:counter-1")
	if e != nil {
		t.Fatal(e)
	}
	j, e := Decode[model.Job](job)
	if e != nil || j.FromMS != now-2000 || j.ToMS != now {
		t.Fatalf("backfill range: %+v %v", j, e)
	}
	q, e := s.Query(ctx, Query{FromMS: now - 3000, ToMS: now, Limit: 100})
	if e != nil || q.Quality.Good != 2 || q.Quality.Excluded != 1 || q.Quality.Missing != nil {
		t.Fatalf("quality: %+v %v", q, e)
	}
	if e = s.BuildRollups(ctx, now-60000, now+1); e != nil {
		t.Fatal(e)
	}
	q, e = s.Query(ctx, Query{FromMS: now - 3600000, ToMS: now, Resolution: "hour", Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	var sum Aggregate
	for _, p := range q.Points {
		b, _ := json.Marshal(p.Value)
		var a Aggregate
		json.Unmarshal(b, &a)
		sum = MergeAggregate(sum, a)
	}
	if sum.Count != 2 || sum.Sum != "40" || sum.Excluded != 1 {
		t.Fatalf("weighted rollup: %+v", sum)
	}
	if _, e = s.Put(ctx, "definition", "permanent", 0, map[string]any{"published": true}); e != nil {
		t.Fatal(e)
	}
	clock := s.Now().AddDate(0, 0, 31)
	s.Now = func() time.Time { return clock }
	counts, e := s.ApplyRetention(ctx, DefaultRetention())
	if e != nil || counts["raw"] != 3 {
		t.Fatalf("retention %+v %v", counts, e)
	}
	if _, e = s.Get(ctx, "definition", "permanent"); e != nil {
		t.Fatal("permanent definition removed", e)
	}
}
func TestDistinctEventsAtSameTimestampAreCounted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, e := s.Ingest(ctx, sample(s, fmt.Sprint(i), 1)); e != nil {
			t.Fatal(e)
		}
	}
	q, e := s.Query(ctx, Query{Limit: 100})
	if e != nil || len(q.Points) != 10 {
		t.Fatalf("same-timestamp events lost: %d %v", len(q.Points), e)
	}
}

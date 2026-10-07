package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func TestDocumentRevisionSQLite(t *testing.T)   { testDocumentRevision(t, false) }
func TestPostgresDocumentRevision(t *testing.T) { testDocumentRevision(t, true) }

func revisionStore(t *testing.T, postgres bool) *store.Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "revision.db")
	if postgres {
		dsn, _ = testdb.Postgres(t, "storage_version_r01")
	}
	s, err := store.Open(context.Background(), dsn, "revision-r01", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if postgres {
		s.DB.SetMaxOpenConns(3)
		s.DB.SetMaxIdleConns(1)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func revisionPut(t *testing.T, s *store.Store, kind, id string, expected int64, value any) store.Document {
	t.Helper()
	d, err := s.Put(context.Background(), kind, id, expected, value)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func revisionDelete(t *testing.T, s *store.Store, kind, id string) {
	t.Helper()
	if err := s.Write(context.Background(), func(tx *store.Tx) error { return tx.Delete(kind, id) }); err != nil {
		t.Fatal(err)
	}
}

func revisionHistory(t *testing.T, s *store.Store, kind, id string) []store.Document {
	t.Helper()
	d, err := s.Versions(context.Background(), kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func revisionAssertHistory(t *testing.T, s *store.Store, kind, id string, want ...int64) {
	t.Helper()
	docs := revisionHistory(t, s, kind, id)
	got := make([]int64, len(docs))
	for i, d := range docs {
		got[i] = d.Version
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history versions %v, want %v", got, want)
	}
}

func testDocumentRevision(t *testing.T, postgres bool) {
	ctx := context.Background()
	t.Run("delete_recreate_preserves_history_cas_and_sync", func(t *testing.T) {
		s := revisionStore(t, postgres)
		value := model.Entity{ID: "recreated", Name: "before"}
		first := revisionPut(t, s, "entity", value.ID, 0, value)
		second := revisionPut(t, s, "entity", value.ID, first.Version, value)
		history := revisionHistory(t, s, "entity", value.ID)
		revisionDelete(t, s, "entity", value.ID)
		if _, err := s.Put(ctx, "entity", value.ID, second.Version, value); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("absent document CAS: %v", err)
		}
		value.Name = "recreated"
		third := revisionPut(t, s, "entity", value.ID, 0, value)
		if third.Version != 3 {
			t.Fatalf("recreated revision %d, want 3", third.Version)
		}
		if _, err := s.Put(ctx, "entity", value.ID, 0, value); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("create over existing: %v", err)
		}
		if _, err := s.Put(ctx, "entity", value.ID, second.Version, value); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("old CAS after recreation: %v", err)
		}
		fourth := revisionPut(t, s, "entity", value.ID, third.Version, value)
		if fourth.Version != 4 {
			t.Fatal(fourth.Version)
		}
		gotHistory := revisionHistory(t, s, "entity", value.ID)
		if !reflect.DeepEqual(gotHistory[:2], history) {
			t.Fatal("retained history changed")
		}
		revisionAssertHistory(t, s, "entity", value.ID, 1, 2, 3, 4)
		changes, err := s.Changes(ctx, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(changes) != 4 {
			t.Fatalf("sync change count %d", len(changes))
		}
		for i, change := range changes {
			if change.Sequence != int64(i+1) || change.Document.Version != int64(i+1) {
				t.Fatalf("sync order %+v", changes)
			}
		}
	})
	t.Run("existing_cas_uses_current_and_allocation_uses_history", func(t *testing.T) {
		s := revisionStore(t, postgres)
		d := revisionPut(t, s, "revision_probe", "older", 0, 1)
		revisionPut(t, s, d.Kind, d.ID, d.Version, 2)
		if _, err := s.DB.ExecContext(ctx, "UPDATE documents SET version=1,data='1' WHERE kind=$1 AND id=$2", d.Kind, d.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(ctx, d.Kind, d.ID, 2, 3); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("CAS must compare current revision: %v", err)
		}
		d = revisionPut(t, s, d.Kind, d.ID, 1, 3)
		if d.Version != 3 {
			t.Fatalf("next revision %d, want 3", d.Version)
		}
		revisionAssertHistory(t, s, d.Kind, d.ID, 1, 2, 3)
	})
	t.Run("ephemeral_recreate_then_actual_configuration_acknowledgement", func(t *testing.T) {
		s := revisionStore(t, postgres)
		p := configcenter.Parameter{ID: "revision-config", Program: "edge", Schema: map[string]any{"type": "integer"}, Value: 1000, Dynamic: true, Version: 1}
		fixed := revisionPut(t, s, "parameter_version", p.ID+":1", 0, p)
		revisionPut(t, s, "parameter", p.ID, 0, p)
		p.Version, p.Value = 2, 2000
		revisionPut(t, s, "parameter", p.ID, 1, p)
		revisionDelete(t, s, "parameter", p.ID)
		p.Version, p.Value = 1, 1000
		if err := s.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral("parameter", p.ID, p) }); err != nil {
			t.Fatal(err)
		}
		d, err := s.Get(ctx, "parameter", p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if d.Version != 3 {
			t.Fatalf("ephemeral local revision %d, want 3", d.Version)
		}
		revisionAssertHistory(t, s, "parameter", p.ID, 1, 2)
		cfg := configcenter.Service{Store: s}
		if err = cfg.Acknowledge(ctx, "edge-a", p.ID, 1, true, ""); err != nil {
			t.Fatal(err)
		}
		d, err = s.Get(ctx, "parameter", p.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.Decode[configcenter.Parameter](d)
		if err != nil {
			t.Fatal(err)
		}
		if d.Version != 4 || got.Version != 1 || got.Effective["edge-a"] != 1 || got.Applications["edge-a"].Version != 1 {
			t.Fatalf("local and fixed revisions: document=%d parameter=%+v", d.Version, got)
		}
		unchanged, err := s.Get(ctx, fixed.Kind, fixed.ID)
		if err != nil || !reflect.DeepEqual(unchanged, fixed) {
			t.Fatalf("fixed configuration changed: %v", err)
		}
		revisionAssertHistory(t, s, "parameter", p.ID, 1, 2, 4)
	})
	t.Run("ephemeral_same_payload_is_noop_then_changed_payload_uses_history", func(t *testing.T) {
		s := revisionStore(t, postgres)
		d := revisionPut(t, s, "revision_probe", "ephemeral", 0, map[string]int{"value": 1})
		revisionPut(t, s, d.Kind, d.ID, 1, map[string]int{"value": 2})
		if _, err := s.DB.ExecContext(ctx, "UPDATE documents SET version=$3,updated_ms=$4,data=$5 WHERE kind=$1 AND id=$2", d.Kind, d.ID, d.Version, d.UpdatedMS, string(d.Data)); err != nil {
			t.Fatal(err)
		}
		history := revisionHistory(t, s, d.Kind, d.ID)
		s.Now = func() time.Time { return time.UnixMilli(d.UpdatedMS + 10000) }
		if err := s.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral(d.Kind, d.ID, map[string]int{"value": 1}) }); err != nil {
			t.Fatal(err)
		}
		unchanged, err := s.Get(ctx, d.Kind, d.ID)
		if err != nil || !reflect.DeepEqual(unchanged, d) {
			t.Fatalf("same payload changed current document: %+v %v", unchanged, err)
		}
		if err = s.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral(d.Kind, d.ID, map[string]int{"value": 3}) }); err != nil {
			t.Fatal(err)
		}
		changed, err := s.Get(ctx, d.Kind, d.ID)
		if err != nil || changed.Version != 3 || changed.UpdatedMS != d.UpdatedMS+10000 {
			t.Fatalf("changed ephemeral %+v %v", changed, err)
		}
		if !reflect.DeepEqual(revisionHistory(t, s, d.Kind, d.ID), history) {
			t.Fatal("ephemeral write changed history")
		}
		if got := revisionPut(t, s, d.Kind, d.ID, 3, map[string]int{"value": 4}); got.Version != 4 {
			t.Fatal(got.Version)
		}
		revisionAssertHistory(t, s, d.Kind, d.ID, 1, 2, 4)
	})
	t.Run("finalize_failure_rolls_back_revision_history_sync_and_audit", func(t *testing.T) {
		s := revisionStore(t, postgres)
		v := model.Entity{ID: "rollback", Name: "before"}
		revisionPut(t, s, "entity", v.ID, 0, v)
		revisionPut(t, s, "entity", v.ID, 1, v)
		revisionDelete(t, s, "entity", v.ID)
		history := revisionHistory(t, s, "entity", v.ID)
		before, err := s.Changes(ctx, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		queries := []string{"CREATE TRIGGER r01_audit_failure BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'r01 deliberate audit failure'); END"}
		if postgres {
			queries = []string{"CREATE FUNCTION r01_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'r01 deliberate audit failure'; END $$", "CREATE TRIGGER r01_audit_failure BEFORE INSERT ON audit FOR EACH ROW EXECUTE FUNCTION r01_audit_failure()"}
		}
		for _, q := range queries {
			if _, err = s.DB.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		err = s.Write(ctx, func(tx *store.Tx) error {
			if _, err := tx.Put("entity", v.ID, 0, v); err != nil {
				return err
			}
			v.Name = "ephemeral rollback"
			if err := tx.SetEphemeral("entity", v.ID, v); err != nil {
				return err
			}
			return tx.Audit(model.Actor{UserID: "r01"}, "revision.rollback", v.ID, "r01", v)
		})
		if err == nil {
			t.Fatal("expected finalize failure")
		}
		if _, err = s.Get(ctx, "entity", v.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("rolled back current document: %v", err)
		}
		if !reflect.DeepEqual(revisionHistory(t, s, "entity", v.ID), history) {
			t.Fatal("rollback changed history")
		}
		after, err := s.Changes(ctx, 0, 100)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("rollback changed sync: %v", err)
		}
		for _, table := range []string{"audit", "audit_checkpoints"} {
			var n int
			if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 0 {
				t.Fatalf("%s count=%d: %v", table, n, err)
			}
		}
		drop := "DROP TRIGGER r01_audit_failure"
		if postgres {
			drop += " ON audit"
		}
		if _, err = s.DB.ExecContext(ctx, drop); err != nil {
			t.Fatal(err)
		}
		if err = s.Write(ctx, func(tx *store.Tx) error {
			d, e := tx.Put("entity", v.ID, 0, v)
			if e != nil {
				return e
			}
			if d.Version != 3 {
				return fmt.Errorf("retry revision %d", d.Version)
			}
			return tx.Audit(model.Actor{UserID: "r01"}, "revision.commit", v.ID, "r01", v)
		}); err != nil {
			t.Fatal(err)
		}
		after, err = s.Changes(ctx, 0, 100)
		if err != nil || len(after) != 3 || after[2].Sequence != 3 || after[2].Document.Version != 3 {
			t.Fatalf("committed sync %v %v", after, err)
		}
		issues, err := s.VerifyAudit(ctx)
		if err != nil || len(issues) != 0 {
			t.Fatalf("audit verification %+v %v", issues, err)
		}
	})
	t.Run("concurrent_recreate_has_one_winner_and_ephemeral_updates_serialize", func(t *testing.T) {
		s := revisionStore(t, postgres)
		revisionPut(t, s, "revision_probe", "contended", 0, 0)
		revisionPut(t, s, "revision_probe", "contended", 1, 1)
		revisionDelete(t, s, "revision_probe", "contended")
		const writers = 8
		results := make(chan error, writers)
		start := make(chan struct{})
		var callbacks atomic.Int64
		var wg sync.WaitGroup
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results <- s.Write(ctx, func(tx *store.Tx) error {
					callbacks.Add(1)
					_, err := tx.Put("revision_probe", "contended", 0, i+2)
					return err
				})
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		success, conflicts := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, store.ErrConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || conflicts != writers-1 || callbacks.Load() != writers {
			t.Fatalf("success=%d conflict=%d callbacks=%d", success, conflicts, callbacks.Load())
		}
		d, err := s.Get(ctx, "revision_probe", "contended")
		if err != nil || d.Version != 3 {
			t.Fatalf("winning document %+v %v", d, err)
		}
		results = make(chan error, writers)
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results <- s.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral(d.Kind, d.ID, i+100) })
			}()
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
		d, err = s.Get(ctx, d.Kind, d.ID)
		if err != nil || d.Version != 3+writers {
			t.Fatalf("serialized ephemeral document %+v %v", d, err)
		}
		last := revisionPut(t, s, d.Kind, d.ID, d.Version, 200)
		if last.Version != 4+writers {
			t.Fatal(last.Version)
		}
		revisionAssertHistory(t, s, d.Kind, d.ID, 1, 2, 3, 4+writers)
	})
	t.Run("import_preserves_source_versions_duplicates_and_out_of_order_history", func(t *testing.T) {
		s := revisionStore(t, postgres)
		d := store.Document{Kind: "revision_probe", ID: "imported", Version: 8, UpdatedMS: 80, Data: json.RawMessage(`{"version":1,"value":1000}`)}
		importDoc := func(want bool, wantErr error, doc store.Document) {
			t.Helper()
			var changed bool
			err := s.Write(ctx, func(tx *store.Tx) error { var e error; changed, e = tx.ImportDocument(doc); return e })
			if !errors.Is(err, wantErr) || changed != want {
				t.Fatalf("import version=%d changed=%v error=%v, want %v %v", doc.Version, changed, err, want, wantErr)
			}
		}
		importDoc(true, nil, d)
		duplicate := d
		duplicate.Data = json.RawMessage(`{"value":1000, "version":1}`)
		duplicate.UpdatedMS = 999
		importDoc(false, nil, duplicate)
		older := d
		older.Version, older.UpdatedMS = 3, 30
		older.Data = json.RawMessage(`{"version":2,"value":2000}`)
		importDoc(false, nil, older)
		conflict := d
		conflict.Data = json.RawMessage(`{"version":1,"value":999}`)
		importDoc(false, store.ErrConflict, conflict)
		current, err := s.Get(ctx, d.Kind, d.ID)
		if err != nil || !reflect.DeepEqual(current, d) {
			t.Fatalf("import current %+v %v", current, err)
		}
		revisionDelete(t, s, d.Kind, d.ID)
		importDoc(false, nil, duplicate)
		if _, err = s.Get(ctx, d.Kind, d.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("retained duplicate behavior changed: %v", err)
		}
		older.Version = 4
		importDoc(true, nil, older)
		next := revisionPut(t, s, d.Kind, d.ID, 4, map[string]int{"version": 2, "value": 2000})
		if next.Version != 9 {
			t.Fatalf("local revision after imported history=%d", next.Version)
		}
		revisionAssertHistory(t, s, d.Kind, d.ID, 3, 4, 8, 9)
		changes, err := s.Changes(ctx, 0, 100)
		if err != nil || len(changes) != 0 {
			t.Fatalf("import created synchronization changes: %+v %v", changes, err)
		}
	})
	t.Run("revision_exhaustion_returns_conflict_without_mutation", func(t *testing.T) {
		s := revisionStore(t, postgres)
		d := store.Document{Kind: "revision_probe", ID: "exhausted", Version: math.MaxInt64, UpdatedMS: 80, Data: json.RawMessage(`1`)}
		if err := s.Write(ctx, func(tx *store.Tx) error { _, e := tx.ImportDocument(d); return e }); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(ctx, d.Kind, d.ID, math.MaxInt64, 2); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("exhausted Put: %v", err)
		}
		if err := s.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral(d.Kind, d.ID, 2) }); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("exhausted ephemeral: %v", err)
		}
		current, err := s.Get(ctx, d.Kind, d.ID)
		if err != nil || !reflect.DeepEqual(current, d) {
			t.Fatalf("exhaustion mutated current: %+v %v", current, err)
		}
		revisionDelete(t, s, d.Kind, d.ID)
		if _, err = s.Put(ctx, d.Kind, d.ID, 0, 2); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("exhausted history: %v", err)
		}
		revisionAssertHistory(t, s, d.Kind, d.ID, math.MaxInt64)
	})
}

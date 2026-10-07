package store

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func pgStore(t *testing.T) *Store {
	t.Helper()
	dsn, _ := testdb.Postgres(t, "storage")
	s, err := Open(context.Background(), dsn, "storage-pg", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.DB.SetMaxOpenConns(3)
	s.DB.SetMaxIdleConns(3)
	t.Cleanup(func() { s.Close() })
	return s
}
func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("parallel transaction did not reach the expected business operation")
	}
}

func TestPostgresIndependentObjectsAndContendedIdentity(t *testing.T) {
	s := pgStore(t)
	ctx := context.Background()
	for _, id := range []string{"one", "two", "counter"} {
		if _, err := s.Put(ctx, "state", id, 0, map[string]int{"n": 0}); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		go func() {
			done <- s.Write(ctx, func(tx *Tx) error {
				if _, err := tx.Get("state", id); err != nil {
					return err
				}
				entered <- struct{}{}
				<-release
				return tx.SetEphemeral("state", id, map[string]int{"n": 1})
			})
		}()
	}
	waitSignal(t, entered)
	waitSignal(t, entered)
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var callbacks atomic.Int64
	var wg sync.WaitGroup
	failures := make(chan error, 18)
	for i := 0; i < 18; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failures <- s.Write(ctx, func(tx *Tx) error {
				callbacks.Add(1)
				doc, err := tx.Get("state", "counter")
				if err != nil {
					return err
				}
				v, err := Decode[map[string]int](doc)
				if err != nil {
					return err
				}
				v["n"]++
				return tx.SetEphemeral("state", "counter", v)
			})
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	doc, _ := s.Get(ctx, "state", "counter")
	count, _ := Decode[map[string]int](doc)
	if count["n"] != 18 || callbacks.Load() != 18 {
		t.Fatal(count, callbacks.Load())
	}
	first := make(chan struct{})
	unlock := make(chan struct{})
	missing := make(chan error, 2)
	go func() {
		missing <- s.Write(ctx, func(tx *Tx) error {
			if _, err := tx.Get("state", "new"); !errors.Is(err, ErrNotFound) {
				return fmt.Errorf("expected missing identity: %w", err)
			}
			close(first)
			<-unlock
			_, err := tx.Put("state", "new", 0, map[string]int{"n": 1})
			return err
		})
	}()
	waitSignal(t, first)
	go func() { _, err := s.Put(ctx, "state", "new", 0, map[string]int{"n": 2}); missing <- err }()
	close(unlock)
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-missing
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
	t.Logf("independent callbacks=2 concurrent; contended increments=18; callbacks=18; missing identity success=%d conflict=%d", success, conflicts)
}

func TestPostgresLateCommitSyncAuditAndAtomicFinalization(t *testing.T) {
	s := pgStore(t)
	ctx := context.Background()
	for _, id := range []string{"slow", "fast"} {
		if _, err := s.Put(ctx, "entity", id, 0, map[string]int{"n": 0}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := s.Changes(ctx, 0, 100)
	cursor := before[len(before)-1].Sequence
	entered := make(chan struct{})
	release := make(chan struct{})
	slow := make(chan error, 1)
	go func() {
		slow <- s.Write(ctx, func(tx *Tx) error {
			if _, err := tx.Put("entity", "slow", 1, map[string]int{"n": 1}); err != nil {
				return err
			}
			if err := tx.Audit(model.Actor{UserID: "writer"}, "slow", "slow", "slow", map[string]int{"n": 1}); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	waitSignal(t, entered)
	if err := s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Put("entity", "fast", 1, map[string]int{"n": 1}); err != nil {
			return err
		}
		return tx.Audit(model.Actor{UserID: "writer"}, "fast", "fast", "fast", nil)
	}); err != nil {
		t.Fatal(err)
	}
	visible, err := s.Changes(ctx, cursor, 100)
	if err != nil || len(visible) != 1 || visible[0].Document.ID != "fast" {
		t.Fatal(visible, err)
	}
	cursor = visible[0].Sequence
	close(release)
	if err = <-slow; err != nil {
		t.Fatal(err)
	}
	later, err := s.Changes(ctx, cursor, 100)
	if err != nil || len(later) != 1 || later[0].Document.ID != "slow" || later[0].Sequence <= cursor {
		t.Fatal(later, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.Audit(ctx, model.Actor{UserID: "parallel"}, "parallel", "r", fmt.Sprint(i), map[string]int{"n": i})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	remote := testStore(t)
	for i := 0; i < 4; i++ {
		if err = remote.Audit(ctx, model.Actor{UserID: "remote"}, "remote", "r", fmt.Sprint(i), nil); err != nil {
			t.Fatal(err)
		}
	}
	transfer, _ := remote.ExportAudit(ctx, 0, 100)
	if _, err = s.ImportAudit(ctx, remote.NodeID, remote.SignKey.Public().(ed25519.PublicKey), transfer); err != nil {
		t.Fatal(err)
	}
	if issues, err := s.VerifyAudit(ctx); err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
	_, err = s.DB.Exec(`CREATE FUNCTION fail_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected finalize failure'; END $$; CREATE TRIGGER reject_audit BEFORE INSERT ON audit FOR EACH ROW EXECUTE FUNCTION fail_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = s.Write(ctx, func(tx *Tx) error {
		calls++
		if _, err := tx.Inbox("atomic-message", "hash", "source"); err != nil {
			return err
		}
		if _, err := tx.Put("entity", "rollback", 0, model.Entity{ID: "rollback"}); err != nil {
			return err
		}
		if err := tx.Enqueue("atomic-outbox", "tb_entity", "rollback", model.Entity{ID: "rollback"}); err != nil {
			return err
		}
		return tx.Audit(model.Actor{}, "reject", "r", "r", nil)
	})
	if err == nil || calls != 1 {
		t.Fatal("finalizer unexpectedly succeeded or callback replayed", err, calls)
	}
	for _, query := range []string{"SELECT count(*) FROM inbox WHERE id='atomic-message'", "SELECT count(*) FROM documents WHERE id='rollback'", "SELECT count(*) FROM document_versions WHERE id='rollback'", "SELECT count(*) FROM sync_changes WHERE id='rollback'", "SELECT count(*) FROM outbox WHERE id='atomic-outbox'", "SELECT count(*) FROM sf_tasks WHERE outbox_id='atomic-outbox'", "SELECT count(*) FROM river_job"} {
		var n int
		if err = s.DB.QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Fatalf("atomic rollback %s=%d: %v", query, n, err)
		}
	}
	t.Logf("committed cursors fast=%d slow=%d; local audit=14 remote=4; finalize rollback includes inbox/doc/history/sync/outbox/River", cursor, later[0].Sequence)
}

func TestPostgresDeadlockIsRecognizableWithoutCallbackReplay(t *testing.T) {
	s := pgStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, id := range []string{"a", "b"} {
		if _, err := s.Put(ctx, "lock", id, 0, map[string]int{"n": 0}); err != nil {
			t.Fatal(err)
		}
	}
	ready := make(chan struct{}, 2)
	goOn := make(chan struct{})
	results := make(chan error, 2)
	var calls atomic.Int64
	for _, ids := range [][2]string{{"a", "b"}, {"b", "a"}} {
		go func() {
			results <- s.Write(ctx, func(tx *Tx) error {
				calls.Add(1)
				if _, err := tx.Get("lock", ids[0]); err != nil {
					return err
				}
				ready <- struct{}{}
				<-goOn
				_, err := tx.Get("lock", ids[1])
				return err
			})
		}()
	}
	waitSignal(t, ready)
	waitSignal(t, ready)
	close(goOn)
	retryable := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if errors.Is(err, ErrRetryable) && errors.Is(err, ErrConflict) {
			retryable++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if retryable != 1 || calls.Load() != 2 {
		t.Fatal(retryable, calls.Load())
	}
	t.Log("PostgreSQL 40P01 returned as ErrRetryable and ErrConflict; 2 calls ran once each")
}

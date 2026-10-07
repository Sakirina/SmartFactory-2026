package store

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"competition2026/product/platform/pkg/model"
	"github.com/riverqueue/river"
)

type visibleTaskWorker struct {
	river.WorkerDefaults[TaskArgs]
	ran chan string
}

func (w *visibleTaskWorker) Work(ctx context.Context, job *river.Job[TaskArgs]) error {
	w.ran <- job.Args.BusinessID
	return nil
}
func testRiverAtomicity(t *testing.T, s *Store) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan string, 3)
	workers := river.NewWorkers()
	river.AddWorker(workers, &visibleTaskWorker{ran: ran})
	client, err := river.NewClient(s.RiverDriver(), &river.Config{Workers: workers, Queues: map[string]river.QueueConfig{"projection": {MaxWorkers: 1}}, PollOnly: true, FetchCooldown: 10 * time.Millisecond, FetchPollInterval: 20 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := client.StopAndCancel(stop); err != nil {
			t.Error(err)
		}
	}()
	rollback := errors.New("business rejected")
	if err = s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Put("entity", "rolled-back", 0, model.Entity{ID: "rolled-back"}); err != nil {
			return err
		}
		if err := tx.Enqueue("rollback", "tb_entity", "rolled-back", model.Entity{ID: "rolled-back"}); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var n int
	if err = s.DB.QueryRow("SELECT count(*) FROM river_job").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	err = s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.Put("entity", "committed", 0, model.Entity{ID: "committed"}); err != nil {
			return err
		}
		if err := tx.Enqueue("commit", "tb_entity", "committed", model.Entity{ID: "committed"}); err != nil {
			return err
		}
		// This is the actual Store *sql.Tx. PostgreSQL's independent reader cannot
		// see its River row; SQLite's single connection also defers acquisition.
		var inside int
		if err := tx.QueryRow("SELECT count(*) FROM river_job").Scan(&inside); err != nil || inside != 1 {
			return errors.New("same transaction cannot see inserted River job")
		}
		if s.Driver == "pgx" {
			var outside int
			if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM river_job").Scan(&outside); err != nil {
				return err
			}
			if outside != 0 {
				return errors.New("uncommitted River job visible outside transaction")
			}
		}
		select {
		case <-ran:
			return errors.New("worker acquired uncommitted job")
		case <-time.After(80 * time.Millisecond):
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-ran:
		if id != "commit" {
			t.Fatal(id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("committed job was not acquired")
	}
	if err = s.Write(ctx, func(tx *Tx) error {
		return tx.Enqueue("commit", "tb_entity", "committed", model.Entity{ID: "committed"})
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow("SELECT count(*) FROM river_job").Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	t.Log("same *sql.Tx insertion; rollback=0 jobs; uncommitted acquisition=0; committed worker acquisition=1; repeated identity=1 job")
}
func TestRiverSQLiteTransactionalEnqueue(t *testing.T)   { testRiverAtomicity(t, testStore(t)) }
func TestPostgresRiverTransactionalEnqueue(t *testing.T) { testRiverAtomicity(t, pgStore(t)) }

func TestExpiredRiverHistoryKeepsBusinessIdentity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var original int64
	err := s.Write(ctx, func(tx *Tx) error {
		var err error
		original, err = tx.EnqueueTask("tb_reconcile", "retained-identity", 0, "", []string{"*"}, time.Time{})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// River's cleaner deletes old finalized rows while the business identity
	// stays durable for deduplication of old deliveries.
	if _, err := s.DB.Exec("DELETE FROM river_job WHERE id=$1", original); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Task(ctx, "tb_reconcile:retained-identity"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	ids, err := s.TaskIDs(ctx, "", 100)
	if err != nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
	err = s.Write(ctx, func(tx *Tx) error {
		current, err := tx.EnqueueTask("tb_reconcile", "retained-identity", 0, "", []string{"*"}, time.Time{})
		if err == nil && current != original {
			return errors.New("expired work was enqueued again")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM river_job").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal(jobs, err)
	}
}

func TestTaskCancellationDoesNotReplaceNewerRecomputeGeneration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	job := model.Job{ID: "backfill:sensor", Kind: "recompute", Status: "pending", DeviceID: "sensor", FromMS: 1, ToMS: 2}
	first, err := s.Put(ctx, "job", job.ID, 0, job)
	if err != nil {
		t.Fatal(err)
	}
	older, _ := Decode[model.Job](first)
	if _, err = s.Put(ctx, "job", job.ID, first.Version, job); err != nil {
		t.Fatal(err)
	}
	task, err := s.Task(ctx, older.TaskID)
	if err != nil || !task.Superseded {
		t.Fatal(task, err)
	}
	changed, err := s.ChangeTask(ctx, task.ID, task.Version, "cancel", model.Actor{UserID: "operator"}, nil)
	if err != nil || changed.State != "cancelled" {
		t.Fatal(changed, err)
	}
	latest, _ := s.Get(ctx, "job", job.ID)
	business, _ := Decode[model.Job](latest)
	if business.Status != "pending" || business.TaskID == older.TaskID {
		t.Fatal(business)
	}
	if _, err = s.ChangeTask(ctx, task.ID, changed.Version, "retry", model.Actor{}, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("superseded retry", err)
	}
}

// Compile-time guard documents that every driver uses the Store transaction type.
var _ *river.Client[*sql.Tx]

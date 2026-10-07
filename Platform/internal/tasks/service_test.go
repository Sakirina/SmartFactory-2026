package tasks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func fixture(t *testing.T, dsn string) *store.Store {
	t.Helper()
	if dsn == "" {
		dsn = filepath.Join(t.TempDir(), "tasks.db")
	}
	s, err := store.Open(context.Background(), dsn, "tasks-fixture", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if s.Driver == "pgx" {
		s.DB.SetMaxOpenConns(3)
	}
	policy := s.Policy()
	policy.Archive.Enabled = false
	s.SetPolicy(policy)
	t.Cleanup(func() { s.Close() })
	return s
}
func start(t *testing.T, s *store.Store, h Handlers) *Service {
	t.Helper()
	service, err := New(s, h)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = service.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return service
}
func until(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not finish within budget", timeout)
}
func projection(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.Write(context.Background(), func(tx *store.Tx) error {
		return tx.Enqueue(id, "tb_entity", "sensor", model.Entity{ID: "sensor", Version: 1})
	}); err != nil {
		t.Fatal(err)
	}
}

func retryAndCancel(t *testing.T, s *store.Store) {
	projection(t, s, "retry-projection")
	projection(t, s, "cancel-projection")
	var attempts atomic.Int64
	entered := make(chan struct{}, 1)
	start(t, s, Handlers{Projection: func(ctx context.Context, d store.Delivery) error {
		if d.ID == "cancel-projection" {
			entered <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}
		if attempts.Add(1) == 1 {
			return errors.New("injected external service failure")
		}
		return nil
	}})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker not started")
	}
	current, err := s.Task(context.Background(), "tb_entity:cancel-projection")
	if err != nil || current.State != "running" {
		t.Fatal(current, err)
	}
	changed, err := s.ChangeTask(context.Background(), current.ID, current.Version, "cancel", model.Actor{UserID: "operator"}, nil)
	if err != nil || changed.CancelRequestedMS == 0 {
		t.Fatal(changed, err)
	}
	until(t, 12*time.Second, func() bool {
		task, err := s.Task(context.Background(), "tb_entity:retry-projection")
		return err == nil && task.State == "completed" && task.Attempt == 2
	})
	until(t, 12*time.Second, func() bool {
		task, err := s.Task(context.Background(), current.ID)
		return err == nil && task.State == "cancelled"
	})
	if _, err = s.Delivery(context.Background(), "retry-projection"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("completed outbox retained", err)
	}
	if _, err = s.Delivery(context.Background(), "cancel-projection"); err != nil {
		t.Fatal("cancelled external identity lost", err)
	}
	task, _ := s.Task(context.Background(), "tb_entity:retry-projection")
	if task.Error == "" || attempts.Load() != 2 {
		t.Fatal(task, attempts.Load())
	}
	t.Log("River external projection: attempts=2, first failure retained, completion/outbox deletion atomic; running cancellation observed; original cancelled outbox retained")
}
func TestSQLiteRiverRetryAndRunningCancellation(t *testing.T) { retryAndCancel(t, fixture(t, "")) }
func TestPostgresRiverRetryAndRunningCancellation(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "river")
	retryAndCancel(t, fixture(t, dsn))
}

func TestTaskCrashProcess(t *testing.T) {
	if os.Getenv("SF_TASK_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	s, err := store.Open(context.Background(), os.Getenv("SF_TASK_CRASH_DSN"), "tasks-fixture", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	handlers := Handlers{Projection: func(context.Context, store.Delivery) error {
		if err := os.WriteFile(os.Getenv("SF_TASK_CRASH_READY"), []byte("running"), 0600); err != nil {
			return err
		}
		select {}
	}}
	if os.Getenv("SF_TASK_CRASH_MODE") == "recompute" {
		handlers = Handlers{Recompute: func(ctx context.Context, job model.Job) error {
			if err := (&engine.Service{Store: s}).Recompute(ctx, job); err == nil {
				return errors.New("checkpoint fault did not occur")
			}
			if err := os.WriteFile(os.Getenv("SF_TASK_CRASH_READY"), []byte("checkpoint persisted"), 0600); err != nil {
				return err
			}
			select {}
		}}
	}
	service, err := New(s, handlers)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestRiverRecoversRecomputeCheckpointAfterProcessKill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recompute-crash.db")
	s, err := store.Open(context.Background(), path, "tasks-fixture", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Minute).UnixMilli()
	definition := model.Definition{ID: "crash-counter", Name: "Crash counter", GroupID: "factory", SchemaVersion: model.ContractVersion, Kind: "analysis", Status: "published", Version: 1, EffectiveMS: base - 1, Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"value"}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "counter", Type: "counter", Params: map[string]any{"mode": "delta"}}}, Connections: []model.Connection{{From: "input", To: "counter"}}, Outputs: []model.Output{{Key: "total", NodeID: "counter", Type: "number"}}}
	if _, err = s.Put(context.Background(), "definition", definition.ID, 0, definition); err != nil {
		t.Fatal(err)
	}
	if prepared, err := (&engine.Service{Store: s}).PreparePublishedPlans(context.Background()); err != nil || len(prepared.Isolated) != 0 {
		t.Fatal(prepared, err)
	}
	if err = s.Write(context.Background(), func(tx *store.Tx) error {
		for index, offset := range []int64{0, 0, 11 * 60000} {
			if err := tx.InsertPoint(model.Observation{ID: fmt.Sprintf("crash-point-%d", index), MessageID: fmt.Sprint(index), DeviceID: "device", Key: "value", Value: 1 << index, SourceSequence: uint64(index), ObservedMS: base + offset, ReceivedMS: base + offset, Quality: "GOOD", Revision: 1}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	job := model.Job{ID: "crash-recompute", Kind: "recompute", Status: "pending", DeviceID: "device", FromMS: base, ToMS: base + 11*60000}
	doc, err := s.Put(context.Background(), "job", job.ID, 0, job)
	if err != nil {
		t.Fatal(err)
	}
	job, _ = store.Decode[model.Job](doc)
	firstCursor := base + 10*60000 - 1
	if _, err = s.DB.Exec(fmt.Sprintf(`CREATE TRIGGER stop_checkpoint BEFORE UPDATE ON documents WHEN NEW.kind='job' AND json_extract(NEW.data,'$.status')='running' AND json_extract(NEW.data,'$.cursor_ms')>%d BEGIN SELECT RAISE(ABORT,'checkpoint stop'); END`, firstCursor)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	ready := filepath.Join(t.TempDir(), "ready")
	log, err := os.Create(filepath.Join(t.TempDir(), "child.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestTaskCrashProcess$")
	command.Env = append(os.Environ(), "SF_TASK_CRASH_HELPER=1", "SF_TASK_CRASH_MODE=recompute", "SF_TASK_CRASH_DSN="+path, "SF_TASK_CRASH_READY="+ready)
	command.Stdout, command.Stderr = log, log
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	until(t, 10*time.Second, func() bool { _, err := os.Stat(ready); return err == nil })
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("helper survived forced termination")
	}
	s = fixture(t, path)
	doc, err = s.Get(context.Background(), "job", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	interrupted, _ := store.Decode[model.Job](doc)
	task, err := s.Task(context.Background(), job.TaskID)
	if err != nil || interrupted.CursorMS != firstCursor || interrupted.ReplayID != job.TaskID || task.State != "running" || task.Attempt != 1 {
		t.Fatal(interrupted, task, err)
	}
	if _, err = s.DB.Exec("DROP TRIGGER stop_checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE river_job SET attempted_at=$1 WHERE id=$2", time.Now().Add(-2*time.Minute).UTC().Round(time.Millisecond).Format("2006-01-02 15:04:05.000"), task.RiverID); err != nil {
		t.Fatal(err)
	}
	start(t, s, Handlers{Recompute: (&engine.Service{Store: s}).Recompute})
	until(t, 40*time.Second, func() bool {
		current, err := s.Task(context.Background(), task.ID)
		return err == nil && current.State == "completed" && current.Attempt == 2
	})
	current, err := s.Task(context.Background(), task.ID)
	if err != nil || current.RiverID != task.RiverID || current.BusinessID != task.BusinessID || current.CursorMS != base+11*60000 {
		t.Fatal(current, err)
	}
	value, err := s.Latest(context.Background(), "device", "crash-counter.total")
	if err != nil || fmt.Sprint(value.Value) != "7" {
		t.Fatal(value, err)
	}
	var count int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM documents WHERE kind='recompute_outputs'").Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
	t.Logf("actual subprocess SIGKILL after cursor=%d; stable task/business/River/replay identity; official rescue attempt2 resumed complete window; 3 unique evaluations including two same-timestamp inputs; final counter=7", firstCursor)
}
func TestRiverRecoversAfterProcessKill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.db")
	s, err := store.Open(context.Background(), path, "tasks-fixture", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	projection(t, s, "crash-projection")
	s.Close()
	ready := filepath.Join(t.TempDir(), "ready")
	logPath := filepath.Join(t.TempDir(), "child.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestTaskCrashProcess$")
	command.Env = append(os.Environ(), "SF_TASK_CRASH_HELPER=1", "SF_TASK_CRASH_DSN="+path, "SF_TASK_CRASH_READY="+ready)
	command.Stdout = log
	command.Stderr = log
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill() }()
	until(t, 10*time.Second, func() bool { _, err := os.Stat(ready); return err == nil })
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("helper did not terminate abruptly")
	}
	s = fixture(t, path)
	task, err := s.Task(context.Background(), "tb_entity:crash-projection")
	if err != nil || task.State != "running" || task.Attempt != 1 {
		t.Fatal(task, err)
	}
	// Only the clock-age of the killed attempt is advanced. The following
	// transition and reclaim are performed by River's production rescuer.
	if _, err = s.DB.Exec("UPDATE river_job SET attempted_at=$1 WHERE id=$2", time.Now().Add(-2*time.Minute).UTC().Round(time.Millisecond).Format("2006-01-02 15:04:05.000"), task.RiverID); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	start(t, s, Handlers{Projection: func(context.Context, store.Delivery) error { calls.Add(1); return nil }})
	until(t, 40*time.Second, func() bool {
		current, err := s.Task(context.Background(), task.ID)
		return err == nil && current.State == "completed" && current.Attempt == 2
	})
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	if _, err = s.Delivery(context.Background(), "crash-projection"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	t.Log("actual child process SIGKILL after durable running attempt; injected age=120s against 90s rescue threshold; official River rescuer completed attempt 2 once")
}

func TestBudgetsKeepIngestionAvailableDuringBacklog(t *testing.T) {
	s := fixture(t, "")
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("backlog-%d", i)
		job := model.Job{ID: id, Kind: "recompute", Status: "pending", DeviceID: "sensor", FromMS: 1, ToMS: 2}
		if _, err := s.Put(ctx, "job", id, 0, job); err != nil {
			t.Fatal(err)
		}
		if err := s.Write(ctx, func(tx *store.Tx) error {
			_, err := tx.EnqueueTask("archive", id, 0, "", []string{"*"}, time.Time{})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		projection(t, s, id)
	}
	var recompute, archive, project atomic.Int64
	block := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	start(t, s, Handlers{Recompute: func(ctx context.Context, job model.Job) error { recompute.Add(1); return block(ctx) }, Archive: func(ctx context.Context) (store.ArchiveStats, error) {
		archive.Add(1)
		return store.ArchiveStats{}, block(ctx)
	}, Projection: func(ctx context.Context, _ store.Delivery) error { project.Add(1); return block(ctx) }})
	until(t, 5*time.Second, func() bool { return recompute.Load() == 1 && archive.Load() == 1 && project.Load() == 2 })
	started := time.Now()
	result, err := s.Ingest(ctx, store.IngestBatch{MessageID: "foreground", SourceID: "collector", Points: []model.Observation{{DeviceID: "fresh", Key: "value", Value: 1, ObservedMS: s.Now().UnixMilli(), Quality: "GOOD"}}})
	elapsed := time.Since(started)
	if err != nil || !result.Committed || elapsed > time.Second {
		t.Fatal(result, elapsed, err)
	}
	budgets, err := s.QueueBudgets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range budgets {
		if budget.Queue == "recompute" || budget.Queue == "archive" || budget.Queue == "projection" {
			if budget.Pending < 1 || budget.Running != int64(budget.Concurrency) {
				t.Fatal(budget)
			}
		}
	}
	t.Logf("24 queued workload records; active recompute=1 archive=1 projection=2; ingress=%s; queues=%+v", elapsed, budgets)
}

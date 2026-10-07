package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"github.com/riverqueue/river/riverdriver"
)

func (f *taskCancelFixture) snoozes() int {
	f.t.Helper()
	var count int
	if raw := f.metadata()["snoozes"]; raw != nil {
		if err := json.Unmarshal(raw, &count); err != nil {
			f.t.Fatal(err)
		}
	}
	return count
}

// Observe multiple completed production snoozes, checking the queue as well as
// the business row. A waiting business row alone missed P3-T02.
func (f *taskCancelFixture) awaitSnoozes(minimum int) model.Task {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task := f.task()
		run := f.state()
		switch task.State {
		case "available", "scheduled", "running", "pending":
		default:
			f.t.Fatalf("retry stopped during production scheduling: queue=%s business=%s snoozes=%d metadata=%s", task.State, run.Status, f.snoozes(), f.metadata())
		}
		if run.Status == "waiting_parent" && f.snoozes() >= minimum {
			if _, present := f.metadata()["cancel_attempted_at"]; present {
				f.t.Fatal("retry retained cancellation key", f.metadata())
			}
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("not enough production snoozes", minimum, f.snoozes(), f.task(), f.state())
	return model.Task{}
}

func (f *taskCancelFixture) assertActionCount(action string, expected int) {
	f.t.Helper()
	events, err := f.server.Store.AuditList(context.Background(), f.run.TaskID, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Action == action {
			count++
		}
	}
	if count != expected {
		f.t.Fatal(action, "audit count", count, "expected", expected)
	}
	if issues, err := f.server.Store.VerifyAudit(context.Background()); err != nil || len(issues) != 0 {
		f.t.Fatal(issues, err)
	}
}

func (f *taskCancelFixture) assertMetadataRetained(before map[string]json.RawMessage) {
	f.t.Helper()
	after := f.metadata()
	for key, raw := range before {
		if key == "cancel_attempted_at" || key == "sf_operation_revision" || key == "snoozes" {
			continue
		}
		if _, present := after[key]; !present {
			f.t.Fatal("metadata key removed by retry", key)
		}
		if store.Hash(json.RawMessage(raw)) != store.Hash(json.RawMessage(after[key])) {
			// PostgreSQL normalizes object whitespace and key order. Compare with
			// UseNumber so arbitrary metadata retains integers above 2^53.
			var left, right any
			if e := store.DecodeJSON(raw, &left); e != nil {
				f.t.Fatal(e)
			}
			if e := store.DecodeJSON(after[key], &right); e != nil || !reflect.DeepEqual(left, right) {
				f.t.Fatalf("metadata %s changed: before=%s after=%s error=%v", key, raw, after[key], e)
			}
		}
	}
}

// Hold the first real handler after it has recorded waiting_parent. This makes
// the HTTP cancellation encounter an actively running River job, then lets the
// original worker's real JobSnooze finish the cancellation.
func cancelledRunningHistory(t *testing.T, postgres bool) (*taskCancelFixture, model.Task, historymodel.Run, map[string]json.RawMessage) {
	t.Helper()
	f := newTaskCancelFixture(t, postgres, true, 1)
	metadata := f.metadata()
	metadata["river:unique_nonce"] = json.RawMessage(`"retained-nonce"`)
	metadata["extension"] = json.RawMessage(`{"big":9007199254740993,"null":null,"nested":{"keep":"value"}}`)
	metadata["custom_null"] = json.RawMessage(`null`)
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.Store.DB.Exec("UPDATE river_job SET metadata=$1 WHERE id=$2", string(encoded), f.task().RiverID); err != nil {
		t.Fatal(err)
	}
	entered, unblock := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(unblock) }) }
	t.Cleanup(release)
	f.start(func(ctx context.Context, id string) error {
		err := f.history.Execute(ctx, id)
		if first.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-unblock:
			case <-ctx.Done():
			}
		}
		return err
	})
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("production history handler did not begin")
	}
	before, run := f.task(), f.state()
	if before.State != "running" || run.Status != "waiting_parent" {
		t.Fatal("cancellation must start from running River and waiting business", before, run)
	}
	f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 200)
	marker := f.metadata()["cancel_attempted_at"]
	if len(marker) == 0 || string(marker) == "null" {
		t.Fatal("running cancellation did not create a timestamp", f.metadata())
	}
	f.assertRevision(1)
	// The original worker still owns this running attempt. Retrying now must
	// leave its cancellation intent intact until it has reported its outcome.
	f.request("POST", "/tasks/"+before.ID+"/retry", model.TaskAction{ExpectedVersion: f.task().Version}, 409)
	release()
	cancelled := f.assertCancelled(before, run.SnapshotID, 0)
	f.assertMetadataRetained(metadata)
	t.Logf("real running cancellation: task=%s river=%d run=%s snapshot=%s marker=%s snoozes=%d", before.ID, before.RiverID, run.ID, run.SnapshotID, marker, f.snoozes())
	return f, cancelled, run, f.metadata()
}

func (f *taskCancelFixture) retryAndObserve(cancelled model.Task, run historymodel.Run, metadata map[string]json.RawMessage) model.Task {
	f.t.Helper()
	priorSnoozes := f.snoozes()
	f.request("POST", "/tasks/"+cancelled.ID+"/retry", model.TaskAction{ExpectedVersion: cancelled.Version}, 200)
	f.request("POST", "/tasks/"+cancelled.ID+"/retry", model.TaskAction{ExpectedVersion: cancelled.Version}, 409)
	retried := f.awaitSnoozes(priorSnoozes + 3)
	f.assertRevision(2)
	f.assertMetadataRetained(metadata)
	current := f.state()
	steps, err := f.server.Store.AnalysisSteps(context.Background(), run.ID, 0, 500)
	if err != nil || retried.ID != cancelled.ID || retried.RiverID != cancelled.RiverID || retried.BusinessID != cancelled.BusinessID || current.ID != run.ID || current.SnapshotID != run.SnapshotID || current.CapturedSnapshotID != run.CapturedSnapshotID || current.Cursor != run.Cursor || len(steps.Items) != run.Cursor {
		f.t.Fatal("retry changed identity, snapshot or committed steps", current, run, retried, steps, err)
	}
	f.assertActionCount("task.retry", 1)
	f.t.Logf("retry200 stayed active through %d additional production snoozes; task=%s river=%d business=%s operation_revision=2", f.snoozes()-priorSnoozes, retried.ID, retried.RiverID, current.Status)
	return retried
}

func (f *taskCancelFixture) cancelRetried(task model.Task) {
	f.t.Helper()
	f.request("POST", "/tasks/"+task.ID+"/cancel", model.TaskAction{ExpectedVersion: task.Version}, 200)
	f.await(func(task model.Task, run historymodel.Run) bool {
		return task.State == "cancelled" && run.Status == "cancelled"
	})
	f.assertRevision(3)
	f.assertActionCount("task.cancel", 2)
	f.request("POST", "/tasks/"+task.ID+"/cancel", model.TaskAction{ExpectedVersion: task.Version}, 409)
	if marker := f.metadata()["cancel_attempted_at"]; len(marker) == 0 || string(marker) == "null" {
		f.t.Fatal("second cancellation lost its new intent", f.metadata())
	}
}

func TestTaskRetryClearsPriorCancellation(t *testing.T) {
	testTaskRetryClearsPriorCancellation(t, false)
}

func TestPostgresTaskRetryClearsPriorCancellation(t *testing.T) {
	testTaskRetryClearsPriorCancellation(t, true)
}

type heldHistoryStepRepository struct {
	application.HistoryRepository
	entered chan struct{}
	release chan struct{}
	held    atomic.Bool
}

func (r *heldHistoryStepRepository) CommitAnalysisStep(ctx context.Context, run historymodel.Run, step historymodel.Step, states map[string]historymodel.State) (historymodel.Run, error) {
	if run.Cursor == 1 && r.held.CompareAndSwap(false, true) {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return historymodel.Run{}, ctx.Err()
		}
	}
	return r.HistoryRepository.CommitAnalysisStep(ctx, run, step, states)
}

func testTaskRetryClearsPriorCancellation(t *testing.T, postgres bool) {
	t.Run("running_cancel_retry_multiple_snoozes_then_cancel", func(t *testing.T) {
		f, cancelled, run, metadata := cancelledRunningHistory(t, postgres)
		retried := f.retryAndObserve(cancelled, run, metadata)
		f.cancelRetried(retried)
	})

	t.Run("retry_audit_failure_restores_marker_business_and_revision", func(t *testing.T) {
		f, cancelled, run, metadata := cancelledRunningHistory(t, postgres)
		before := f.state()
		create := `CREATE TRIGGER reject_t02_retry BEFORE INSERT ON audit WHEN json_extract(NEW.data,'$.action')='task.retry' BEGIN SELECT RAISE(ABORT,'injected retry audit failure'); END`
		if postgres {
			if _, err := f.server.Store.DB.Exec(`CREATE FUNCTION reject_t02_retry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data::jsonb->>'action'='task.retry' THEN RAISE EXCEPTION 'injected retry audit failure'; END IF; RETURN NEW; END $$`); err != nil {
				t.Fatal(err)
			}
			create = `CREATE TRIGGER reject_t02_retry BEFORE INSERT ON audit FOR EACH ROW EXECUTE FUNCTION reject_t02_retry()`
		}
		if _, err := f.server.Store.DB.Exec(create); err != nil {
			t.Fatal(err)
		}
		if _, err := f.server.TaskApplication().Change(context.Background(), f.principal, cancelled.ID, cancelled.Version, "retry"); err == nil {
			t.Fatal("retry audit failure was not returned")
		}
		after := f.task()
		if after.Version != cancelled.Version || after.State != "cancelled" || after.CancelRequestedMS != cancelled.CancelRequestedMS || !reflect.DeepEqual(f.state(), before) || !reflect.DeepEqual(f.metadata(), metadata) {
			t.Fatal("failed retry partially committed", cancelled, after, before, f.state(), metadata, f.metadata())
		}
		f.assertRevision(1)
		f.assertActionCount("task.retry", 0)
		drop := "DROP TRIGGER reject_t02_retry"
		if postgres {
			drop += " ON audit"
		}
		if _, err := f.server.Store.DB.Exec(drop); err != nil {
			t.Fatal(err)
		}
		f.cancelRetried(f.retryAndObserve(cancelled, run, metadata))
	})

	t.Run("retry_authorization_and_obsolete_generation", func(t *testing.T) {
		f, cancelled, run, metadata := cancelledRunningHistory(t, postgres)
		f.request("POST", "/tasks/"+cancelled.ID+"/retry", model.TaskAction{ExpectedVersion: "obsolete-version"}, 409)
		ctx := context.Background()
		doc, err := f.server.Store.Get(ctx, "user", "engineer")
		if err != nil {
			t.Fatal(err)
		}
		user, err := store.Decode[model.User](doc)
		if err != nil {
			t.Fatal(err)
		}
		user.Roles = []string{"viewer"}
		user.Version = doc.Version + 1
		viewer, err := f.server.Store.Put(ctx, "user", user.ID, doc.Version, user)
		if err != nil {
			t.Fatal(err)
		}
		f.request("POST", "/tasks/"+cancelled.ID+"/retry", model.TaskAction{ExpectedVersion: cancelled.Version}, 403)
		user.Roles = []string{"engineer"}
		user.Version = viewer.Version + 1
		if _, err := f.server.Store.Put(ctx, "user", user.ID, viewer.Version, user); err != nil {
			t.Fatal(err)
		}
		f.principal, err = f.server.Identity.Authenticate(ctx, f.token)
		if err != nil {
			t.Fatal(err)
		}
		repository := &cancelMutationRepository{TaskRepository: f.server.Store, before: func() error {
			_, err := f.server.Store.Put(ctx, "grant", "concurrent-retry-grant", 0, map[string]any{"id": "concurrent-retry-grant"})
			return err
		}}
		useCase := &application.Tasks{Store: repository, Identity: f.server.Identity}
		if _, err := useCase.Change(ctx, f.principal, cancelled.ID, cancelled.Version, "retry"); !errors.Is(err, store.ErrConflict) {
			t.Fatal("retry accepted concurrent authorization change", err)
		}
		if f.task().Version != cancelled.Version || !reflect.DeepEqual(f.metadata(), metadata) {
			t.Fatal("rejected retry changed cancellation", f.task(), f.metadata())
		}
		f.cancelRetried(f.retryAndObserve(cancelled, run, metadata))
	})

	t.Run("concurrent_retries_commit_one_new_operation", func(t *testing.T) {
		f, cancelled, run, metadata := cancelledRunningHistory(t, postgres)
		priorSnoozes := f.snoozes()
		body, err := json.Marshal(model.TaskAction{ExpectedVersion: cancelled.Version})
		if err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			code int
			err  error
		}
		start, results := make(chan struct{}), make(chan outcome, 2)
		for range 2 {
			go func() {
				<-start
				req, err := http.NewRequest("POST", f.http.URL+"/api/sf/v1/tasks/"+cancelled.ID+"/retry", bytes.NewReader(body))
				if err != nil {
					results <- outcome{err: err}
					return
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+f.token)
				response, err := f.http.Client().Do(req)
				if err != nil {
					results <- outcome{err: err}
					return
				}
				_, err = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				results <- outcome{code: response.StatusCode, err: err}
			}()
		}
		close(start)
		codes := map[int]int{}
		for range 2 {
			result := <-results
			if result.err != nil {
				t.Fatal(result.err)
			}
			codes[result.code]++
		}
		if codes[200] != 1 || codes[409] != 1 {
			t.Fatal("concurrent retry responses", codes)
		}
		retried := f.awaitSnoozes(priorSnoozes + 3)
		if retried.ID != cancelled.ID || retried.RiverID != cancelled.RiverID || f.state().SnapshotID != run.SnapshotID {
			t.Fatal("concurrent retry replaced the run", retried, f.state())
		}
		f.assertMetadataRetained(metadata)
		f.assertRevision(2)
		f.assertActionCount("task.retry", 1)
		f.cancelRetried(retried)
	})

	t.Run("cancel_old_worker_commit_retry_preserves_completed_steps", func(t *testing.T) {
		f := newTaskCancelFixture(t, postgres, false, 3)
		gate := &heldHistoryStepRepository{HistoryRepository: f.server.Store, entered: make(chan struct{}), release: make(chan struct{})}
		var once sync.Once
		release := func() { once.Do(func() { close(gate.release) }) }
		t.Cleanup(release)
		f.history.Store = gate
		f.start(nil)
		select {
		case <-gate.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not reach second real commit")
		}
		before, run := f.task(), f.state()
		steps, err := f.server.Store.AnalysisSteps(context.Background(), run.ID, 0, 500)
		if err != nil || before.State != "running" || run.Cursor != 1 || len(steps.Items) != 1 {
			t.Fatal(before, run, steps, err)
		}
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 200)
		if f.metadata()["cancel_attempted_at"] == nil {
			t.Fatal("running cancellation marker missing")
		}
		f.request("POST", "/tasks/"+before.ID+"/retry", model.TaskAction{ExpectedVersion: f.task().Version}, 409)
		release()
		cancelled := f.assertCancelled(before, run.SnapshotID, 1)
		f.request("POST", "/tasks/"+before.ID+"/retry", model.TaskAction{ExpectedVersion: cancelled.Version}, 200)
		finished := f.await(func(task model.Task, run historymodel.Run) bool {
			return task.State == "completed" && run.Status == "completed"
		})
		after, err := f.server.Store.AnalysisSteps(context.Background(), run.ID, 0, 500)
		if err != nil || len(after.Items) != 3 || store.Hash(after.Items[0]) != store.Hash(steps.Items[0]) || f.state().SnapshotID != run.SnapshotID || finished.ID != before.ID || finished.RiverID != before.RiverID || finished.BusinessID != before.BusinessID {
			t.Fatal("retry lost or duplicated committed steps", before, finished, steps, after, err)
		}
		for index, step := range after.Items {
			if step.InputIndex != index {
				t.Fatal("duplicate or missing step", after)
			}
		}
		if _, present := f.metadata()["cancel_attempted_at"]; present {
			t.Fatal("completed retry retained cancellation intent", f.metadata())
		}
		f.assertRevision(2)
		f.assertActionCount("task.retry", 1)
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 409)
		f.request("POST", "/tasks/"+before.ID+"/retry", model.TaskAction{ExpectedVersion: finished.Version}, 409)
		t.Logf("old worker completion rejected after cancellation; same task=%s river=%d snapshot=%s resumed cursor1 and completed three unique steps", before.ID, before.RiverID, run.SnapshotID)
	})

	t.Run("official_null_merge_differs_but_retry_removes_key", func(t *testing.T) {
		f, cancelled, run, metadata := cancelledRunningHistory(t, postgres)
		ctx := context.Background()
		tx, err := f.server.Store.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = f.server.Store.TaskClient.Driver().UnwrapExecutor(tx).JobUpdate(ctx, &riverdriver.JobUpdateParams{ID: cancelled.RiverID, MetadataDoMerge: true, Metadata: []byte(`{"cancel_attempted_at":null}`)})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		_, present := f.metadata()["cancel_attempted_at"]
		if present != postgres {
			t.Fatal("unexpected official JSON null merge behavior", postgres, present, f.metadata())
		}
		t.Logf("official JobUpdate merge null: postgres=%v key_present=%v; retry must leave key absent on either database", postgres, present)
		f.cancelRetried(f.retryAndObserve(cancelled, run, metadata))
	})
}

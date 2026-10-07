package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/tasks"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

type taskCancelFixture struct {
	t         *testing.T
	server    *Server
	http      *httptest.Server
	token     string
	principal identity.Principal
	run       historymodel.Run
	history   *application.History
}

func newTaskCancelFixture(t *testing.T, postgres, waiting bool, total int) *taskCancelFixture {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "task-cancel.db")
	if postgres {
		dsn, _ = testdb.Postgres(t, "task_cancel_t01")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn, "task-cancel-t01", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.DB.SetMaxOpenConns(3)
	if !postgres {
		db.DB.SetMaxOpenConns(1)
	}
	db.DB.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	for _, entity := range []model.Entity{{ID: "factory", Kind: "asset"}, {ID: "other", Kind: "asset"}, {ID: "device", Kind: "device", ParentID: "factory"}, {ID: "forbidden", Kind: "device", ParentID: "other"}} {
		if _, err := db.Put(ctx, "entity", entity.ID, 0, entity); err != nil {
			t.Fatal(err)
		}
	}
	auth := &identity.Manager{Store: db, Master: make([]byte, 32)}
	if _, err = auth.CreateUser(ctx, model.Actor{}, model.User{ID: "engineer", Name: "Task engineer", Login: "engineer", Active: true, Roles: []string{"engineer"}, Resources: []string{"factory"}}, "task-test-password-1234", "", 0); err != nil {
		t.Fatal(err)
	}
	token, principal, err := auth.Login(ctx, "engineer", "task-test-password-1234", "", false, "task-cancel-test")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, Identity: auth, Engine: &engine.Service{Store: db}, Mode: "cloud"}
	base := time.Now().Add(-time.Minute).UnixMilli()
	definition := model.Definition{ID: "task-counter", Name: "Task counter", Kind: "analysis", SchemaVersion: model.ContractVersion, GroupID: "factory", Status: "published", Version: 1, EffectiveMS: base - 1, Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"count"}}, Nodes: []model.Node{{ID: "value", Type: "expression", Params: map[string]any{"code": "value+1"}}}, Outputs: []model.Output{{NodeID: "value", Key: "result", Type: "number"}}}
	if _, err := db.Put(ctx, "definition", definition.ID, 0, definition); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.PrepareDefinition(ctx, definition); err != nil {
		t.Fatal(err)
	}
	points := make([]model.Observation, total)
	for index := range points {
		points[index] = model.Observation{ID: fmt.Sprintf("input-%d", index), MessageID: fmt.Sprintf("message-%d", index), DeviceID: "device", Key: "count", Value: index + 1, ObservedMS: base + int64(index), ReceivedMS: base + int64(index), Quality: "GOOD", Revision: 1}
	}
	hist := s.HistoryApplication()
	run, err := hist.Create(ctx, principal, historymodel.Request{ID: "confirmed-run", Kind: "replay", DefinitionID: definition.ID, FromMS: base, ToMS: base + int64(total), Points: points, History: []model.Observation{}})
	if err != nil {
		t.Fatal(err)
	}
	if waiting {
		parent := historymodel.Run{ID: "previous-run", Status: "failed", Error: "previous generation failed", FinalState: map[string]historymodel.State{"candidate": {}}}
		if _, err = db.Put(ctx, "analysis_run", parent.ID, 0, parent); err != nil {
			t.Fatal(err)
		}
		run.ParentRunID, run.InitialStateResolved = parent.ID, false
		doc, err := db.Put(ctx, "analysis_run", run.ID, run.Version, run)
		if err != nil {
			t.Fatal(err)
		}
		run.Version = doc.Version
	}
	task, err := db.Task(ctx, run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately keep the legacy metadata shape without an operation revision.
	// Later worker writes must retain both these existing fields and the revision.
	metadata := `{"sf_trace":{"traceparent":"00-11111111111111111111111111111111-2222222222222222-01","tracestate":"test=retained"},"fixture":{"retained":true}}`
	if _, err := db.DB.Exec("UPDATE river_job SET metadata=$1 WHERE id=$2", metadata, task.RiverID); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(s.Handler())
	t.Cleanup(httpServer.Close)
	return &taskCancelFixture{t: t, server: s, http: httpServer, token: token, principal: principal, run: run, history: hist}
}

func (f *taskCancelFixture) start(handler func(context.Context, string) error) {
	f.t.Helper()
	if handler == nil {
		handler = f.history.Execute
	}
	worker, err := tasks.New(f.server.Store, tasks.Handlers{Analysis: handler})
	if err != nil {
		f.t.Fatal(err)
	}
	if err = worker.Start(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := worker.Stop(ctx); err != nil {
			f.t.Error(err)
		}
	})
}

func (f *taskCancelFixture) request(method, path string, body any, expected int) []byte {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	req, err := http.NewRequest(method, f.http.URL+"/api/sf/v1"+path, bytes.NewReader(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	response, err := f.http.Client().Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != expected {
		f.t.Fatalf("%s %s: status=%d expected=%d body=%s error=%v", method, path, response.StatusCode, expected, result, err)
	}
	return result
}

func (f *taskCancelFixture) task() model.Task {
	f.t.Helper()
	var task model.Task
	if err := store.DecodeJSON(f.request("GET", "/tasks/"+f.run.TaskID, nil, 200), &task); err != nil {
		f.t.Fatal(err)
	}
	return task
}

func (f *taskCancelFixture) state() historymodel.Run {
	f.t.Helper()
	run, err := f.server.Store.AnalysisRun(context.Background(), f.run.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f *taskCancelFixture) await(check func(model.Task, historymodel.Run) bool) model.Task {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task, err := f.server.Store.Task(context.Background(), f.run.TaskID)
		if err != nil {
			f.t.Fatal(err)
		}
		if check(task, f.state()) {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("task did not reach expected state", f.task(), f.state())
	return model.Task{}
}

func (f *taskCancelFixture) metadata() map[string]json.RawMessage {
	f.t.Helper()
	task, err := f.server.Store.Task(context.Background(), f.run.TaskID)
	if err != nil {
		f.t.Fatal(err)
	}
	row, err := f.server.Store.TaskClient.JobGet(context.Background(), task.RiverID)
	if err != nil {
		f.t.Fatal(err)
	}
	var metadata map[string]json.RawMessage
	if err = json.Unmarshal(row.Metadata, &metadata); err != nil {
		f.t.Fatal(err)
	}
	return metadata
}

func (f *taskCancelFixture) assertRevision(expected uint64) {
	f.t.Helper()
	metadata := f.metadata()
	var revision uint64
	if raw := metadata["sf_operation_revision"]; raw != nil {
		if err := json.Unmarshal(raw, &revision); err != nil {
			f.t.Fatal(err)
		}
	}
	var fixture struct {
		Retained bool `json:"retained"`
	}
	if err := json.Unmarshal(metadata["fixture"], &fixture); err != nil || !fixture.Retained || revision != expected {
		f.t.Fatalf("operation metadata lost: %s revision=%d expected=%d", metadata, revision, expected)
	}
	var trace map[string]string
	if err := json.Unmarshal(metadata["sf_trace"], &trace); err != nil || trace["tracestate"] != "test=retained" {
		f.t.Fatal("trace metadata lost", trace, err)
	}
}

func (f *taskCancelFixture) completeParent() {
	f.t.Helper()
	ctx := context.Background()
	doc, err := f.server.Store.Get(ctx, "analysis_run", f.run.ParentRunID)
	if err != nil {
		f.t.Fatal(err)
	}
	parent, err := store.Decode[historymodel.Run](doc)
	if err != nil {
		f.t.Fatal(err)
	}
	parent.Status = "completed"
	if _, err = f.server.Store.Put(ctx, "analysis_run", parent.ID, doc.Version, parent); err != nil {
		f.t.Fatal(err)
	}
}

func (f *taskCancelFixture) assertCancelled(before model.Task, snapshot string, expectedCursor int) model.Task {
	f.t.Helper()
	current := f.await(func(task model.Task, run historymodel.Run) bool {
		return task.State == "cancelled" && run.Status == "cancelled"
	})
	run := f.state()
	if current.ID != before.ID || current.RiverID != before.RiverID || current.BusinessID != before.BusinessID || run.ID != f.run.ID || run.SnapshotID != snapshot || run.Cursor != expectedCursor {
		f.t.Fatal("cancellation changed identity or committed results", before, current, run)
	}
	time.Sleep(650 * time.Millisecond)
	after := f.state()
	steps, err := f.server.Store.AnalysisSteps(context.Background(), run.ID, 0, 500)
	if err != nil || after.Version != run.Version || after.Cursor != run.Cursor || len(steps.Items) != expectedCursor {
		f.t.Fatal("work committed after cancellation", run, after, len(steps.Items), err)
	}
	events, err := f.server.Store.AuditList(context.Background(), before.ID, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	cancelCount := 0
	for _, event := range events {
		if event.Action == "task.cancel" {
			cancelCount++
		}
	}
	if cancelCount != 1 {
		f.t.Fatal("cancel audit count", cancelCount)
	}
	if issues, err := f.server.Store.VerifyAudit(context.Background()); err != nil || len(issues) != 0 {
		f.t.Fatal(issues, err)
	}
	return current
}

// Delays only test observation between actual committed steps. The production
// history worker, 500ms snooze, evaluator and transaction paths stay unchanged.
type pacedHistoryRepository struct {
	application.HistoryRepository
	afterBegin func(historymodel.Run)
}

func (r *pacedHistoryRepository) BeginAnalysis(ctx context.Context, id string) (historymodel.Run, error) {
	run, err := r.HistoryRepository.BeginAnalysis(ctx, id)
	if err == nil && r.afterBegin != nil {
		r.afterBegin(run)
	}
	return run, err
}

func (r *pacedHistoryRepository) CommitAnalysisStep(ctx context.Context, run historymodel.Run, step historymodel.Step, states map[string]historymodel.State) (historymodel.Run, error) {
	next, err := r.HistoryRepository.CommitAnalysisStep(ctx, run, step, states)
	if err == nil {
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	return next, err
}

type cancelMutationRepository struct {
	application.TaskRepository
	before func() error
}

func (r *cancelMutationRepository) ChangeTask(ctx context.Context, id, expected, action string, actor model.Actor, guard func(*store.Tx) error) (model.Task, error) {
	if err := r.before(); err != nil {
		return model.Task{}, err
	}
	return r.TaskRepository.ChangeTask(ctx, id, expected, action, actor, guard)
}

func TestTaskOperationVersionsAllowHumanConfirmation(t *testing.T) {
	testTaskOperationVersions(t, false)
}

func TestPostgresTaskOperationVersionsAllowHumanConfirmation(t *testing.T) {
	testTaskOperationVersions(t, true)
}

func testTaskOperationVersions(t *testing.T, postgres bool) {
	t.Run("waiting_parent_delayed_cancel_and_retry", func(t *testing.T) {
		f := newTaskCancelFixture(t, postgres, true, 1)
		f.start(nil)
		f.await(func(_ model.Task, run historymodel.Run) bool { return run.Status == "waiting_parent" })
		f.assertRevision(0)
		before, run := f.task(), f.state()
		metadataBefore := f.metadata()
		start := time.Now()
		time.Sleep(3 * time.Second)
		after := f.task()
		if before.Version != after.Version || f.state().Version != run.Version || string(f.metadata()["snoozes"]) == string(metadataBefore["snoozes"]) {
			t.Fatal("scheduler changed operation version or did not run", before, after, metadataBefore, f.metadata())
		}
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 200)
		cancelled := f.assertCancelled(before, run.SnapshotID, 0)
		f.assertRevision(1)
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 409)
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: cancelled.Version}, 409)
		snoozesBeforeRetry := f.snoozes()
		f.request("POST", "/tasks/"+before.ID+"/retry", model.TaskAction{ExpectedVersion: cancelled.Version}, 200)
		retried := f.awaitSnoozes(snoozesBeforeRetry + 3)
		f.assertRevision(2)
		if retried.ID != before.ID || retried.RiverID != before.RiverID || f.state().SnapshotID != run.SnapshotID || retried.Version == before.Version {
			t.Fatal("retry changed identity or reused operation revision", before, retried)
		}
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 409)
		f.cancelRetried(retried)
		t.Logf("confirmation >=3s; elapsed=%s task=%s river=%d run=%s snapshot=%s snoozes=%s; cancel200 duplicate409 retry200 stale-generation409 metadata revision2 retained", time.Since(start), before.ID, before.RiverID, run.ID, run.SnapshotID, f.metadata()["snoozes"])
	})

	t.Run("running_progress_delayed_cancel", func(t *testing.T) {
		f := newTaskCancelFixture(t, postgres, false, 80)
		f.history.Store = &pacedHistoryRepository{HistoryRepository: f.server.Store}
		f.start(nil)
		f.await(func(_ model.Task, run historymodel.Run) bool { return run.Status == "running" && run.Cursor >= 1 })
		before, run := f.task(), f.state()
		time.Sleep(3 * time.Second)
		after, progressed := f.task(), f.state()
		if before.Version != after.Version || progressed.Version <= run.Version || progressed.Cursor <= run.Cursor || progressed.Status != "running" {
			t.Fatal("progress changed the cancel operation", before, after, run, progressed)
		}
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 200)
		cancelled := f.state()
		f.assertCancelled(before, run.SnapshotID, cancelled.Cursor)
		f.assertRevision(1)
		t.Logf("3s confirmation while actual committed progress advanced cursor%d->%d version%d->%d; cancel retained %d steps and snapshot%s", run.Cursor, progressed.Cursor, run.Version, progressed.Version, cancelled.Cursor, run.SnapshotID)
	})

	t.Run("resolved_parent_snapshot_rejects_old_version", func(t *testing.T) {
		f := newTaskCancelFixture(t, postgres, true, 1)
		resolved, resume := make(chan struct{}), make(chan struct{})
		var once, releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(resume) }) }
		defer release()
		f.history.Store = &pacedHistoryRepository{HistoryRepository: f.server.Store, afterBegin: func(run historymodel.Run) {
			if run.InitialStateResolved {
				once.Do(func() { close(resolved); <-resume })
			}
		}}
		f.start(nil)
		f.await(func(_ model.Task, run historymodel.Run) bool { return run.Status == "waiting_parent" })
		before := f.task()
		f.completeParent()
		select {
		case <-resolved:
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not resolve parent")
		}
		current, run := f.task(), f.state()
		if current.Version == before.Version || run.SnapshotID == f.run.SnapshotID || !run.InitialStateResolved || run.Status != "running" {
			t.Fatal("resolved snapshot retained old operation", before, current, run)
		}
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 409)
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: current.Version}, 200)
		release()
		f.assertCancelled(before, run.SnapshotID, 0)
		f.assertRevision(1)
	})

	t.Run("parent_completed_child_still_waiting_and_worker_metadata", func(t *testing.T) {
		f := newTaskCancelFixture(t, postgres, true, 1)
		waiting, resume := make(chan struct{}), make(chan struct{})
		var once, releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(resume) }) }
		defer release()
		f.start(func(ctx context.Context, id string) error {
			err := f.history.Execute(ctx, id)
			if errors.Is(err, historymodel.ErrParentPending) {
				once.Do(func() { close(waiting); <-resume })
			}
			return err
		})
		select {
		case <-waiting:
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not wait for parent")
		}
		before := f.task()
		f.completeParent()
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 200)
		release()
		f.assertCancelled(before, f.run.SnapshotID, 0)
		if f.state().InitialStateResolved {
			t.Fatal("parent resolution passed cancellation")
		}
		f.assertRevision(1)
		if f.metadata()["snoozes"] == nil {
			t.Fatal("worker snooze merge was not exercised")
		}
	})

	t.Run("concurrent_confirmations_commit_one_cancel", func(t *testing.T) {
		f := newTaskCancelFixture(t, postgres, true, 1)
		f.start(nil)
		f.await(func(_ model.Task, run historymodel.Run) bool { return run.Status == "waiting_parent" })
		before := f.task()
		start := make(chan struct{})
		type result struct {
			code int
			err  error
		}
		results := make(chan result, 2)
		for range 2 {
			go func() {
				<-start
				raw, err := json.Marshal(model.TaskAction{ExpectedVersion: before.Version})
				if err != nil {
					results <- result{err: err}
					return
				}
				req, err := http.NewRequest("POST", f.http.URL+"/api/sf/v1/tasks/"+before.ID+"/cancel", bytes.NewReader(raw))
				if err != nil {
					results <- result{err: err}
					return
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+f.token)
				response, err := f.http.Client().Do(req)
				if err != nil {
					results <- result{err: err}
					return
				}
				_, err = io.Copy(io.Discard, response.Body)
				response.Body.Close()
				results <- result{code: response.StatusCode, err: err}
			}()
		}
		close(start)
		codes := map[int]int{}
		for range 2 {
			value := <-results
			if value.err != nil {
				t.Fatal(value.err)
			}
			codes[value.code]++
		}
		if codes[200] != 1 || codes[409] != 1 {
			t.Fatal(codes)
		}
		f.assertCancelled(before, f.run.SnapshotID, 0)
		f.assertRevision(1)
	})

	t.Run("terminal_state_rejects_old_version", func(t *testing.T) {
		f := newTaskCancelFixture(t, postgres, false, 1)
		before := f.task()
		f.start(nil)
		f.await(func(task model.Task, run historymodel.Run) bool {
			return task.State == "completed" && run.Status == "completed"
		})
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 409)
		f.assertRevision(0)
	})

	for _, mutation := range []string{"role", "resource_ancestry", "task_resources_after_authorization", "grant_after_authorization"} {
		t.Run(mutation, func(t *testing.T) {
			f := newTaskCancelFixture(t, postgres, true, 1)
			f.start(nil)
			f.await(func(_ model.Task, run historymodel.Run) bool { return run.Status == "waiting_parent" })
			before := f.task()
			ctx := context.Background()
			switch mutation {
			case "role":
				doc, err := f.server.Store.Get(ctx, "user", "engineer")
				if err != nil {
					t.Fatal(err)
				}
				user, err := store.Decode[model.User](doc)
				if err != nil {
					t.Fatal(err)
				}
				user.Roles = []string{"viewer"}
				if _, err := f.server.Store.Put(ctx, "user", user.ID, doc.Version, user); err != nil {
					t.Fatal(err)
				}
				f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 403)
			case "resource_ancestry":
				doc, err := f.server.Store.Get(ctx, "entity", "device")
				if err != nil {
					t.Fatal(err)
				}
				entity, err := store.Decode[model.Entity](doc)
				if err != nil {
					t.Fatal(err)
				}
				entity.ParentID = "other"
				if _, err := f.server.Store.Put(ctx, "entity", entity.ID, doc.Version, entity); err != nil {
					t.Fatal(err)
				}
				f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 403)
			default:
				repository := &cancelMutationRepository{TaskRepository: f.server.Store, before: func() error {
					if mutation == "grant_after_authorization" {
						_, err := f.server.Store.Put(ctx, "grant", "concurrent-grant", 0, map[string]any{"id": "concurrent-grant"})
						return err
					}
					_, err := f.server.Store.DB.Exec("UPDATE sf_tasks SET resources=$1 WHERE id=$2", `["forbidden"]`, before.ID)
					return err
				}}
				useCase := &application.Tasks{Store: repository, Identity: f.server.Identity}
				if _, err := useCase.Change(ctx, f.principal, before.ID, before.Version, "cancel"); !errors.Is(err, store.ErrConflict) {
					t.Fatal("concurrent authorization/resource mutation passed", err)
				}
			}
			if f.state().Status != "waiting_parent" {
				t.Fatal(f.state())
			}
			f.assertRevision(0)
		})
	}

	t.Run("audit_failure_rolls_back_cancel_and_operation_revision", func(t *testing.T) {
		f := newTaskCancelFixture(t, postgres, true, 1)
		f.start(nil)
		f.await(func(_ model.Task, run historymodel.Run) bool { return run.Status == "waiting_parent" })
		before, run := f.task(), f.state()
		create := `CREATE TRIGGER reject_t01_audit BEFORE INSERT ON audit WHEN json_extract(NEW.data,'$.action')='task.cancel' BEGIN SELECT RAISE(ABORT,'injected cancellation audit failure'); END`
		if postgres {
			if _, err := f.server.Store.DB.Exec(`CREATE FUNCTION reject_t01_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.data::jsonb->>'action'='task.cancel' THEN RAISE EXCEPTION 'injected cancellation audit failure'; END IF; RETURN NEW; END $$`); err != nil {
				t.Fatal(err)
			}
			create = `CREATE TRIGGER reject_t01_audit BEFORE INSERT ON audit FOR EACH ROW EXECUTE FUNCTION reject_t01_audit()`
		}
		if _, err := f.server.Store.DB.Exec(create); err != nil {
			t.Fatal(err)
		}
		if _, err := f.server.TaskApplication().Change(context.Background(), f.principal, before.ID, before.Version, "cancel"); err == nil {
			t.Fatal("injected audit failure was not returned")
		}
		after := f.state()
		if after.Version != run.Version || after.Status != run.Status || f.task().CancelRequestedMS != 0 {
			t.Fatal("failed audit left cancellation changes", run, after)
		}
		f.assertRevision(0)
		if events, err := f.server.Store.AuditList(context.Background(), before.ID, 100); err != nil || len(events) != 0 {
			t.Fatal("failed audit persisted task action", events, err)
		}
		drop := "DROP TRIGGER reject_t01_audit"
		if postgres {
			drop += " ON audit"
		}
		if _, err := f.server.Store.DB.Exec(drop); err != nil {
			t.Fatal(err)
		}
		f.request("POST", "/tasks/"+before.ID+"/cancel", model.TaskAction{ExpectedVersion: before.Version}, 200)
		f.assertCancelled(before, run.SnapshotID, 0)
		f.assertRevision(1)
	})
}

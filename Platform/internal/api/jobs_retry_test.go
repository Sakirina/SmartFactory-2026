package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/tasks"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func legacyTaskRunner(t *testing.T, s *Server) func() {
	t.Helper()
	worker, err := tasks.New(s.Store, tasks.Handlers{Recompute: s.Engine.Recompute, Archive: func(context.Context) (store.ArchiveStats, error) { return store.ArchiveStats{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := worker.Stop(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}
func awaitLegacyTask(t *testing.T, s *Server, id string, check func(model.Task) bool) model.Task {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		task, err := s.Store.Task(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if check(task) {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, _ := s.Store.Task(context.Background(), id)
	t.Fatal("task did not reach expected state", task)
	return task
}
func legacyFailedCheckpoint(t *testing.T, phase string) (*Server, string, model.Job, model.Task) {
	t.Helper()
	s, token := scopedServer(t, false)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour).Truncate(time.Minute).UnixMilli()
	last := base + 11*60000
	if phase == "rollups" {
		last = base + 25*3600000
	}
	definition := model.Definition{ID: "legacy-counter", Name: "Legacy counter", GroupID: "a", SchemaVersion: model.ContractVersion, Kind: "analysis", Status: "published", Version: 1, EffectiveMS: base - 1, Selector: model.Selector{DeviceIDs: []string{"device-a"}, Keys: []string{"value"}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "counter", Type: "counter", Params: map[string]any{"mode": "delta"}}}, Connections: []model.Connection{{From: "input", To: "counter"}}, Outputs: []model.Output{{Key: "total", NodeID: "counter", Type: "number"}}}
	if _, err := s.Store.Put(ctx, "definition", definition.ID, 0, definition); err != nil {
		t.Fatal(err)
	}
	if prepared, err := s.Engine.PreparePublishedPlans(ctx); err != nil || len(prepared.Isolated) != 0 {
		t.Fatal(prepared, err)
	}
	if err := s.Store.Write(ctx, func(tx *store.Tx) error {
		for i, at := range []int64{base, base, last} {
			if err := tx.InsertPoint(model.Observation{ID: fmt.Sprint("legacy-raw-", i), MessageID: fmt.Sprint("legacy-input-", i), DeviceID: "device-a", Key: "value", Value: 1 << i, SourceSequence: uint64(i), ObservedMS: at, ReceivedMS: at, Quality: "GOOD", Revision: 1}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	job := model.Job{ID: "legacy-checkpoint", Kind: "recompute", Status: "pending", DeviceID: "device-a", FromMS: base, ToMS: last}
	doc, err := s.Store.Put(ctx, "job", job.ID, 0, job)
	if err != nil {
		t.Fatal(err)
	}
	job, _ = store.Decode[model.Job](doc)
	trigger := fmt.Sprintf(`CREATE TRIGGER stop_legacy BEFORE UPDATE ON documents WHEN NEW.kind='job' AND json_extract(NEW.data,'$.status')='running' AND json_extract(NEW.data,'$.cursor_ms')>%d BEGIN SELECT RAISE(ABORT,'injected legacy checkpoint interruption'); END`, base+10*60000-1)
	if phase == "rollups" {
		day := time.UnixMilli(base).UTC().Truncate(24 * time.Hour).Add(24 * time.Hour).UnixMilli()
		trigger = fmt.Sprintf(`CREATE TRIGGER stop_legacy BEFORE INSERT ON rollups WHEN NEW.bucket_ms>=%d BEGIN SELECT RAISE(ABORT,'injected legacy rollup interruption'); END`, day)
	}
	if _, err = s.Store.DB.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	stop := legacyTaskRunner(t, s)
	failed := awaitLegacyTask(t, s, job.TaskID, func(task model.Task) bool { return task.State == "retryable" && task.BusinessState == "failed" })
	stop()
	doc, err = s.Store.Get(ctx, "job", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	job, _ = store.Decode[model.Job](doc)
	job.Version = doc.Version
	if job.ReplayPhase != phase || job.CursorMS == 0 || job.ReplayID == "" {
		t.Fatal(job)
	}
	return s, token, job, failed
}
func TestLegacyJobRetryResumesOriginalCheckpointAndRollupPhase(t *testing.T) {
	for _, phase := range []string{"replaying", "rollups"} {
		t.Run(phase, func(t *testing.T) {
			s, token, failed, before := legacyFailedCheckpoint(t, phase)
			stateBefore, err := s.Store.List(context.Background(), "engine_state")
			if err != nil {
				t.Fatal(err)
			}
			w := call(s, token, http.MethodPost, "/api/sf/v1/jobs/"+url.PathEscape(failed.ID)+"/retry", nil)
			var response model.Job
			if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
				t.Fatal(w.Code, w.Body.String())
			}
			if response.ID != failed.ID || response.TaskID != failed.TaskID || response.ReplayID != failed.ReplayID || response.ReplayPhase != phase || response.CursorMS != failed.CursorMS || response.Progress != failed.Progress || response.ReplayStartMS != failed.ReplayStartMS || response.ReplayEndMS != failed.ReplayEndMS {
				t.Fatal("legacy retry reset checkpoint", failed, response)
			}
			current, err := s.Store.Task(context.Background(), failed.TaskID)
			if err != nil || current.RiverID != before.RiverID || current.State != "available" {
				t.Fatal(current, err)
			}
			if _, err = s.Store.DB.Exec("DROP TRIGGER stop_legacy"); err != nil {
				t.Fatal(err)
			}
			stop := legacyTaskRunner(t, s)
			completed := awaitLegacyTask(t, s, failed.TaskID, func(task model.Task) bool { return task.State == "completed" })
			stop()
			point, err := s.Store.Latest(context.Background(), "device-a", "legacy-counter.total")
			if err != nil || fmt.Sprint(point.Value) != "7" || completed.RiverID != before.RiverID || completed.Phase != "completed" || completed.ReplayID != failed.ReplayID {
				t.Fatal(point, completed, err)
			}
			var count int
			if err = s.Store.DB.QueryRow("SELECT COUNT(*) FROM documents WHERE kind='recompute_outputs'").Scan(&count); err != nil || count != 3 {
				t.Fatal(count, err)
			}
			if phase == "rollups" {
				after, err := s.Store.List(context.Background(), "engine_state")
				if err != nil || store.Hash(after) != store.Hash(stateBefore) {
					t.Fatal("rollup retry rewrote live state", after, err)
				}
			}
			t.Logf("legacy HTTP202 Job retained task=%s river=%d replay=%s phase=%s cursor=%d; actual River retry completed 3 unique evaluations and final counter7", before.ID, before.RiverID, failed.ReplayID, phase, failed.CursorMS)
		})
	}
}
func TestLegacyJobRetryAdoptsUnmappedJobAtomicallyAndChecksAllResources(t *testing.T) {
	for _, mode := range []string{"adopt", "forbidden", "mapped-forbidden", "viewer", "audit-failure"} {
		t.Run(mode, func(t *testing.T) {
			s, token := scopedServer(t, false)
			ctx := context.Background()
			definition := model.Definition{ID: "legacy-rule", GroupID: "a", Selector: model.Selector{DeviceIDs: []string{"device-a"}}}
			if mode == "forbidden" || mode == "mapped-forbidden" {
				definition.Selector.DeviceIDs = []string{"device-b"}
			}
			if _, err := s.Store.Put(ctx, "definition", definition.ID, 0, definition); err != nil {
				t.Fatal(err)
			}
			job := model.Job{ID: "old-failed", Kind: "recompute", Status: "failed", DeviceID: "device-a", DefinitionID: definition.ID, FromMS: 1, ToMS: 2, CursorMS: 1, Progress: .5}
			if mode == "mapped-forbidden" {
				job.Status = "pending"
			}
			doc, err := s.Store.Put(ctx, "job", job.ID, 0, job)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "mapped-forbidden" {
				job, _ = store.Decode[model.Job](doc)
				job.Status = "failed"
				doc, err = s.Store.Put(ctx, "job", job.ID, doc.Version, job)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "audit-failure" {
				if _, err := s.Store.DB.Exec(`CREATE TRIGGER reject_legacy_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'injected adoption audit failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "viewer" {
				userDoc, err := s.Store.Get(ctx, "user", "user")
				if err != nil {
					t.Fatal(err)
				}
				user, _ := store.Decode[model.User](userDoc)
				user.Roles = []string{"viewer"}
				if _, err := s.Store.Put(ctx, "user", "user", userDoc.Version, user); err != nil {
					t.Fatal(err)
				}
			}
			w := call(s, token, http.MethodPost, "/api/sf/v1/jobs/"+job.ID+"/retry", nil)
			expected := 202
			if mode == "forbidden" || mode == "mapped-forbidden" || mode == "viewer" {
				expected = 403
			}
			if mode == "audit-failure" {
				expected = 400
			}
			if w.Code != expected {
				t.Fatal(mode, w.Code, w.Body.String())
			}
			after, err := s.Store.Get(ctx, "job", job.ID)
			if err != nil {
				t.Fatal(err)
			}
			var riverJobs, links int
			if err = s.Store.DB.QueryRow("SELECT COUNT(*) FROM sf_tasks").Scan(&links); err != nil {
				t.Fatal(err)
			}
			if err = s.Store.DB.QueryRow("SELECT COUNT(*) FROM river_job").Scan(&riverJobs); err != nil {
				t.Fatal(err)
			}
			if mode == "adopt" {
				adopted, _ := store.Decode[model.Job](after)
				if adopted.TaskID == "" || adopted.Status != "running" || adopted.CursorMS != 1 || adopted.Progress != .5 || links != 1 || riverJobs != 1 {
					t.Fatal(adopted, links, riverJobs)
				}
				if again := call(s, token, http.MethodPost, "/api/sf/v1/jobs/"+job.ID+"/retry", nil); again.Code != 409 {
					t.Fatal(again.Code, again.Body.String())
				}
			} else {
				if after.Version != doc.Version || string(after.Data) != string(doc.Data) {
					t.Fatal("denied or failed adoption changed business", doc, after)
				}
				want := 0
				if mode == "mapped-forbidden" {
					want = 1
				}
				if links != want || riverJobs != want {
					t.Fatal(links, riverJobs)
				}
			}
		})
	}
}
func TestLegacyAndTaskRetryConcurrentRequestsShareOneAttempt(t *testing.T) {
	s, token := scopedServer(t, false)
	testLegacyConcurrentRetry(t, s, token)
}

func TestPostgresLegacyAndTaskRetryConcurrentRequestsShareOneAttempt(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "legacy_retry")
	database, err := store.Open(context.Background(), dsn, "legacy-retry", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database.DB.SetMaxOpenConns(3)
	t.Cleanup(func() { database.Close() })
	manager := &identity.Manager{Store: database, Master: make([]byte, 32)}
	if _, err := manager.CreateUser(context.Background(), model.Actor{}, model.User{ID: "operator", Login: "operator", Name: "Operator", Roles: []string{"engineer"}, Resources: []string{"*"}, Active: true}, "test-password-1234", "", 0); err != nil {
		t.Fatal(err)
	}
	token, _, err := manager.Login(context.Background(), "operator", "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	testLegacyConcurrentRetry(t, &Server{Store: database, Identity: manager}, token)
}

func testLegacyConcurrentRetry(t *testing.T, s *Server, token string) {
	t.Helper()
	ctx := context.Background()
	doc, err := s.Store.Put(ctx, "job", "concurrent-retry", 0, model.Job{ID: "concurrent-retry", Kind: "recompute", Status: "pending", DeviceID: "device-a", FromMS: 1, ToMS: 2})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := store.Decode[model.Job](doc)
	job.Status = "failed"
	if _, err = s.Store.Put(ctx, "job", job.ID, doc.Version, job); err != nil {
		t.Fatal(err)
	}
	task, err := s.Store.Task(ctx, job.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.TaskClient.JobCancel(ctx, task.RiverID); err != nil {
		t.Fatal(err)
	}
	task, err = s.Store.Task(ctx, job.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	start := make(chan struct{})
	responses := make(chan int, 2)
	for _, path := range []string{"/jobs/" + url.PathEscape(job.ID) + "/retry", "/tasks/" + url.PathEscape(task.ID) + "/retry"} {
		go func(path string) {
			body, _ := json.Marshal(model.TaskAction{ExpectedVersion: task.Version})
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/sf/v1"+path, bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			<-start
			response, err := server.Client().Do(request)
			if err != nil {
				responses <- 0
				return
			}
			defer response.Body.Close()
			responses <- response.StatusCode
		}(path)
	}
	close(start)
	a, b := <-responses, <-responses
	if !((a == 200 || a == 202) && b == 409 || (b == 200 || b == 202) && a == 409) {
		t.Fatal(a, b)
	}
	current, err := s.Store.Task(ctx, task.ID)
	if err != nil || current.ID != task.ID || current.RiverID != task.RiverID || current.State != "available" {
		t.Fatal(current, err)
	}
	var count int
	if err = s.Store.DB.QueryRow("SELECT COUNT(*) FROM sf_tasks WHERE business_id=$1", job.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	t.Logf("concurrent actual HTTP old/new retry responses=%d/%d; same task/River identity; one mutation committed", a, b)
}
func TestLegacyJobRetryReturnsNotFoundForExpiredRiverAttempt(t *testing.T) {
	s, token := scopedServer(t, false)
	ctx := context.Background()
	doc, err := s.Store.Put(ctx, "job", "expired-retry", 0, model.Job{ID: "expired-retry", Kind: "recompute", Status: "pending", DeviceID: "device-a", FromMS: 1, ToMS: 2})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := store.Decode[model.Job](doc)
	job.Status = "failed"
	if _, err = s.Store.Put(ctx, "job", job.ID, doc.Version, job); err != nil {
		t.Fatal(err)
	}
	task, err := s.Store.Task(ctx, job.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec("DELETE FROM river_job WHERE id=$1", task.RiverID); err != nil {
		t.Fatal(err)
	}
	if w := call(s, token, http.MethodPost, "/api/sf/v1/jobs/"+job.ID+"/retry", nil); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}

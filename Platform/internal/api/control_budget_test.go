package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/tasks"
	"competition2026/product/platform/pkg/model"
)

type budgetDispatcher struct {
	entered chan string
	release chan struct{}
	active  atomic.Int64
	maximum atomic.Int64
}

func (d *budgetDispatcher) Send(ctx context.Context, _ model.Step, id string, _ int64) (control.DispatchResult, error) {
	n := d.active.Add(1)
	defer d.active.Add(-1)
	for old := d.maximum.Load(); n > old; old = d.maximum.Load() {
		if d.maximum.CompareAndSwap(old, n) {
			break
		}
	}
	d.entered <- id
	select {
	case <-ctx.Done():
		return control.DispatchResult{}, ctx.Err()
	case <-d.release:
		return control.DispatchResult{Status: "SUCCESS"}, nil
	}
}

func TestHTTPAndBackgroundControlShareBudgetDuringTaskBacklog(t *testing.T) {
	s, token := scopedServer(t, false)
	ctx := context.Background()
	policy := s.Store.Policy()
	policy.Confirmations.Leaders = 0
	policy.Archive.Enabled = false
	s.Store.SetPolicy(policy)
	dispatcher := &budgetDispatcher{entered: make(chan string, 4), release: make(chan struct{})}
	s.Control = &control.Service{Store: s.Store, Identity: s.Identity, Definitions: s.Engine, Dispatcher: dispatcher, NodeID: "edge-a", Edge: true}
	s.Mode = "edge"
	definition := model.Definition{ID: "budget-control", Kind: "strategy", Status: "published", Version: 1, GroupID: "a", Policy: model.Policy{RiskCategory: "business", RiskLevel: 1, EdgeIDs: []string{"edge-a"}, Steps: []model.Step{{ID: "step", DeviceID: "device-a", EdgeID: "edge-a", Action: "actuate", TimeoutMS: 10000}}}}
	if _, err := s.Store.Put(ctx, "definition", definition.ID, 0, definition); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		job := model.Job{ID: fmt.Sprintf("budget-job-%d", i), Kind: "recompute", Status: "pending", DeviceID: "device-a", FromMS: 1, ToMS: 2}
		if _, err := s.Store.Put(ctx, "job", job.ID, 0, job); err != nil {
			t.Fatal(err)
		}
		if err := s.Store.Write(ctx, func(tx *store.Tx) error {
			_, err := tx.EnqueueTask("archive", fmt.Sprint(i), 0, "", []string{"*"}, time.Time{})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	backlog := make(chan string, 2)
	workerStarted := time.Now()
	logTasks := func() {
		ids, e := s.Store.TaskIDs(ctx, "", 100)
		if e != nil {
			t.Logf("task diagnostic: %v", e)
			return
		}
		for _, id := range ids {
			task, e := s.Store.Task(ctx, id)
			t.Logf("task diagnostic id=%s queue=%s state=%s next_ms=%d attempt=%d error=%v", id, task.Queue, task.State, task.NextMS, task.Attempt, e)
		}
	}
	service, err := tasks.New(s.Store, tasks.Handlers{Recompute: func(ctx context.Context, _ model.Job) error { backlog <- "recompute"; <-ctx.Done(); return ctx.Err() }, Archive: func(ctx context.Context) (store.ArchiveStats, error) {
		backlog <- "archive"
		<-ctx.Done()
		return store.ArchiveStats{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	logTasks()
	workerStarted = time.Now()
	if err = service.Start(workerCtx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancelWorkers()
		stop, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := service.Stop(stop); err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 2; i++ {
		select {
		case kind := <-backlog:
			t.Logf("backlog handler %s entered after %s", kind, time.Since(workerStarted))
		case <-time.After(5 * time.Second):
			logTasks()
			t.Fatal("backlog worker was not active")
		}
	}
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	post := func(path string, body any) model.Execution {
		t.Helper()
		raw, _ := json.Marshal(body)
		request, _ := http.NewRequest(http.MethodPost, httpServer.URL+path, bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var execution model.Execution
		if err = json.NewDecoder(response.Body).Decode(&execution); err != nil || response.StatusCode != 200 {
			t.Fatal(response.Status, execution, err)
		}
		return execution
	}
	post("/api/sf/v1/executions", map[string]any{"definition_id": definition.ID, "downlink_id": "http-budget"})
	post("/api/sf/v1/executions/http-budget/approve", map[string]string{"role": "engineer"})
	started := time.Now()
	manual := post("/api/sf/v1/executions/http-budget/dispatch", nil)
	httpElapsed := time.Since(started)
	if manual.Status != "queued" || httpElapsed > time.Second {
		t.Fatal(manual, httpElapsed)
	}
	delivery, err := s.Store.Delivery(ctx, "downlink:http-budget")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DecodeJSON(delivery.Payload, &manual); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 3)
	go func() {
		_, err := s.Control.Run(ctx, manual, false)
		if err == nil {
			err = s.Store.CompleteDeliveries(ctx, []string{delivery.ID}, nil)
		}
		completed <- err
	}()
	for i := 0; i < 2; i++ {
		request := model.Execution{DownlinkID: fmt.Sprintf("automatic-budget-%d", i), DefinitionID: definition.ID, DefinitionVersion: 1, Status: "queued", Mode: "automatic", Actor: model.Actor{UserID: "published-policy", Source: "edge-a"}, StartDeadlineMS: time.Now().Add(time.Minute).UnixMilli()}
		go func() { _, err := s.Control.Run(ctx, request, true); completed <- err }()
	}
	for i := 0; i < 3; i++ {
		select {
		case <-dispatcher.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("control did not acquire its reserved budget")
		}
	}
	queuedCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	_, err = s.Control.Run(queuedCtx, model.Execution{DownlinkID: "waiting-budget"}, true)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err = s.Store.Get(ctx, "execution", "waiting-budget"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("waiting request consumed database work", err)
	}
	started = time.Now()
	result, err := s.Store.Ingest(ctx, store.IngestBatch{MessageID: "budget-ingress", SourceID: "edge-a", Points: []model.Observation{{DeviceID: "device-a", Key: "fresh", Value: 1, ObservedMS: time.Now().UnixMilli(), Quality: "GOOD"}}})
	ingestElapsed := time.Since(started)
	if err != nil || !result.Committed || ingestElapsed > time.Second {
		t.Fatal(result, ingestElapsed, err)
	}
	budgets, err := s.Store.QueueBudgets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range budgets {
		if budget.Queue == "control" && (budget.Running != 3 || budget.Concurrency != 3) {
			t.Fatal(budget)
		}
	}
	close(dispatcher.release)
	for i := 0; i < 3; i++ {
		if err := <-completed; err != nil {
			t.Fatal(err)
		}
	}
	if dispatcher.maximum.Load() != 3 {
		t.Fatal(dispatcher.maximum.Load())
	}
	t.Logf("real HTTP approval/dispatch queued in %s; manual delivery plus2 automatic executions shared max3; fourth waiter cancelled before DB work; ingestion committed in %s while archive/recompute remained backlogged", httpElapsed, ingestElapsed)
}

package application

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/tasks"
	"competition2026/product/platform/pkg/model"
)

type crashHistoryRepository struct {
	HistoryRepository
	mode, target string
}

func signalHistoryCrash() {
	if err := os.WriteFile(os.Getenv("SF_HISTORY_CRASH_READY"), []byte("persisted"), 0600); err != nil {
		panic(err)
	}
	select {}
}
func (s *crashHistoryRepository) BeginAnalysis(ctx context.Context, id string) (historymodel.Run, error) {
	run, err := s.HistoryRepository.BeginAnalysis(ctx, id)
	if err == nil && id == s.target && s.mode == "resolved-shadow" {
		signalHistoryCrash()
	}
	return run, err
}
func (s *crashHistoryRepository) CommitAnalysisStep(ctx context.Context, run historymodel.Run, step historymodel.Step, states map[string]historymodel.State) (historymodel.Run, error) {
	saved, err := s.HistoryRepository.CommitAnalysisStep(ctx, run, step, states)
	if err == nil && run.ID == s.target && step.InputIndex == 0 && s.mode == "checkpoint" {
		signalHistoryCrash()
	}
	return saved, err
}

// Child processes use production stores, workers and calculation entry points.
// The pause hooks only expose the exact durable stage to the parent SIGKILL.
func TestHistoryCrashProcess(t *testing.T) {
	if os.Getenv("SF_HISTORY_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, os.Getenv("SF_HISTORY_CRASH_DSN"), "history-test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.DB.SetMaxOpenConns(1)
	eng := &engine.Service{Store: db}
	hist := &History{Store: db, Engine: eng, Identity: &identity.Manager{Store: db}}
	mode := os.Getenv("SF_HISTORY_CRASH_MODE")
	if mode == "checkpoint" || mode == "resolved-shadow" {
		hist.Store = &crashHistoryRepository{HistoryRepository: db, mode: mode, target: os.Getenv("SF_HISTORY_CRASH_TARGET")}
		worker, err := tasks.New(db, tasks.Handlers{Analysis: hist.Execute})
		if err != nil {
			t.Fatal(err)
		}
		if err = worker.Start(ctx); err != nil {
			t.Fatal(err)
		}
		select {}
	}
	eng.ObserveAnalysis = func(ctx context.Context, point model.Observation) error {
		if mode == "before-capture" {
			signalHistoryCrash()
		}
		if err := hist.Observe(ctx, point); err != nil {
			return err
		}
		if mode == "after-capture" {
			signalHistoryCrash()
		}
		return nil
	}
	deliveries, err := db.Deliveries(ctx, "engine", 100)
	if err != nil || len(deliveries) == 0 {
		t.Fatal(deliveries, err)
	}
	for _, delivery := range deliveries {
		var point model.Observation
		if err = store.DecodeJSON(delivery.Payload, &point); err != nil {
			t.Fatal(err)
		}
		if err = eng.ProcessCalculations(ctx, point, point.Late); err != nil {
			t.Fatal(err)
		}
		if mode == "before-ack" {
			signalHistoryCrash()
		}
	}
	t.Fatal("crash stage not reached")
}

func killHistoryChild(t *testing.T, path, mode, target string) {
	t.Helper()
	dir := t.TempDir()
	ready, logPath := filepath.Join(dir, "ready"), filepath.Join(dir, "child.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	child := exec.Command(os.Args[0], "-test.run=^TestHistoryCrashProcess$")
	child.Env = append(os.Environ(), "SF_HISTORY_CRASH_HELPER=1", "SF_HISTORY_CRASH_DSN="+path, "SF_HISTORY_CRASH_READY="+ready, "SF_HISTORY_CRASH_MODE="+mode, "SF_HISTORY_CRASH_TARGET="+target)
	child.Stdout, child.Stderr = log, log
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(ready); err == nil {
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = child.Wait(); err == nil {
				t.Fatal("child survived SIGKILL")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	raw, _ := os.ReadFile(logPath)
	t.Fatalf("child did not reach %s: %s", mode, raw)
}
func reopenHistoryFixture(t *testing.T, f *historyFixture) {
	t.Helper()
	db, err := store.Open(context.Background(), f.path, "history-test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.DB.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	f.db, f.engine = db, &engine.Service{Store: db}
	f.history = &History{Store: db, Engine: f.engine, Identity: &identity.Manager{Store: db}}
	f.engine.ObserveAnalysis = f.history.Observe
}
func startHistoryWorker(t *testing.T, f *historyFixture) {
	t.Helper()
	worker, err := tasks.New(f.db, tasks.Handlers{Analysis: f.history.Execute})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := worker.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
}
func waitHistoryTask(t *testing.T, f *historyFixture, id string, attempt int) model.Task {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	var last model.Task
	for time.Now().Before(deadline) {
		var err error
		last, err = f.db.Task(context.Background(), id)
		if err == nil && last.State == "completed" && last.Attempt == attempt {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task did not complete: %+v", last)
	return last
}

func TestHistorySIGKILLResumesCommittedCheckpointUsingOfficialRiverRescue(t *testing.T) {
	f := newHistoryFixture(t, "")
	ctx := context.Background()
	f.publish(t, f.rule(1, "value"))
	run, err := f.history.Create(ctx, f.principal, historymodel.Request{ID: "crash-replay", Kind: "replay", DefinitionID: "counter-rule", FromMS: f.base + 2000, ToMS: f.base + 3000, Points: []model.Observation{f.point("a", 2000, 1), f.point("b", 2000, 2), f.point("c", 3000, 4)}, History: []model.Observation{}})
	if err != nil {
		t.Fatal(err)
	}
	f.db.Close()
	killHistoryChild(t, f.path, "checkpoint", run.ID)
	reopenHistoryFixture(t, f)
	interrupted, err := f.db.AnalysisRun(ctx, run.ID)
	if err != nil || interrupted.Cursor != 1 || interrupted.Status != "running" || interrupted.SnapshotSHA256 != run.SnapshotSHA256 {
		t.Fatal(interrupted, err)
	}
	task, err := f.db.Task(ctx, run.TaskID)
	if err != nil || task.State != "running" || task.Attempt != 1 {
		t.Fatal(task, err)
	}
	// Advance only the killed attempt's age; River performs the rescue transition.
	if _, err = f.db.DB.Exec("UPDATE river_job SET attempted_at=$1 WHERE id=$2", time.Now().Add(-2*time.Minute).UTC().Round(time.Millisecond).Format("2006-01-02 15:04:05.000"), task.RiverID); err != nil {
		t.Fatal(err)
	}
	before := rulecore.SnapshotStatistics()
	startHistoryWorker(t, f)
	current := waitHistoryTask(t, f, task.ID, 2)
	completed, err := f.db.AnalysisRun(ctx, run.ID)
	if err != nil || completed.Cursor != 3 || completed.SnapshotSHA256 != run.SnapshotSHA256 || completed.CapturedSnapshotID != run.CapturedSnapshotID || current.RiverID != task.RiverID || current.BusinessID != task.BusinessID {
		t.Fatal(completed, current, err)
	}
	steps, err := f.db.AnalysisSteps(ctx, run.ID, 0, 100)
	if err != nil || len(steps.Items) != 3 || fmt.Sprint(steps.Items[2].Lanes[0].Evaluation.Values["counter"]) != "7" || rulecore.SnapshotStatistics().Executions-before.Executions != 2 {
		t.Fatal(steps, err)
	}
	t.Logf("SIGKILL cursor=1; River official rescue attempt1->2 age=120s threshold=90s; task=%s river=%d run=%s captured=%s resolved=%s input=%s resumed_executions=2 steps=3 final_counter=7", task.ID, task.RiverID, run.ID, completed.CapturedSnapshotID, completed.SnapshotSHA256, completed.InputSHA256)
}

func TestShadowSIGKILLPreservesResolvedParentSnapshot(t *testing.T) {
	f := newHistoryFixture(t, "")
	ctx := context.Background()
	f.publish(t, f.rule(1, "value+1"))
	f.publish(t, f.rule(2, "value"))
	if _, err := f.history.EnableShadow(ctx, f.principal, historymodel.ShadowRequest{ID: "resolved", DefinitionID: "counter-rule", Version: 1}); err != nil {
		t.Fatal(err)
	}
	f.ingestShadow(t, "first", 99000, 7)
	f.deliverEngine(t, true)
	f.executeShadowGeneration(t, "resolved", 1, 1)
	f.ingestShadow(t, "second", 99001, 3)
	f.deliverEngine(t, true)
	id := "shadow:resolved:1:2"
	captured, err := f.db.AnalysisRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f.db.Close()
	killHistoryChild(t, f.path, "resolved-shadow", id)
	reopenHistoryFixture(t, f)
	resolved, err := f.db.AnalysisRun(ctx, id)
	if err != nil || !resolved.InitialStateResolved || resolved.CapturedSnapshotID != captured.SnapshotID || resolved.SnapshotID == captured.SnapshotID || resolved.Cursor != 0 {
		t.Fatal(resolved, err)
	}
	snapshot, err := f.db.AnalysisSnapshot(ctx, resolved.SnapshotID)
	if err != nil || snapshot.InitialStateSource != "previous_shadow_run" || snapshot.InitialState["device"]["counter"].Count != 8 {
		t.Fatal(snapshot, err)
	}
	task, err := f.db.Task(ctx, resolved.TaskID)
	if err != nil || task.State != "running" || task.Attempt != 1 {
		t.Fatal(task, err)
	}
	if _, err = f.db.DB.Exec("UPDATE river_job SET attempted_at=$1 WHERE id=$2", time.Now().Add(-2*time.Minute).UTC().Round(time.Millisecond).Format("2006-01-02 15:04:05.000"), task.RiverID); err != nil {
		t.Fatal(err)
	}
	before := rulecore.SnapshotStatistics()
	startHistoryWorker(t, f)
	current := waitHistoryTask(t, f, task.ID, 2)
	completed, err := f.db.AnalysisRun(ctx, id)
	if err != nil || completed.SnapshotID != resolved.SnapshotID || completed.SnapshotSHA256 != resolved.SnapshotSHA256 || completed.CapturedSnapshotID != captured.SnapshotID || current.RiverID != task.RiverID || completed.FinalState["candidate"]["device"]["counter"].Count != 12 || rulecore.SnapshotStatistics().Executions-before.Executions != 1 {
		t.Fatal(completed, current, err)
	}
	t.Logf("SIGKILL resolved shadow task=%s river=%d parent=%s captured=%s resolved_before=%s resolved_after=%s initial=8 final=12 resumed_executions=1", task.ID, task.RiverID, completed.ParentRunID, captured.SnapshotSHA256, resolved.SnapshotSHA256, completed.SnapshotSHA256)
}

func TestShadowSIGKILLAtProductionDeliveryStages(t *testing.T) {
	for _, mode := range []string{"before-capture", "after-capture", "before-ack"} {
		t.Run(mode, func(t *testing.T) {
			f := newHistoryFixture(t, "")
			ctx := context.Background()
			f.publish(t, f.rule(1, "value+1"))
			f.publish(t, f.rule(2, "value"))
			if _, err := f.history.EnableShadow(ctx, f.principal, historymodel.ShadowRequest{ID: "crash-candidate", DefinitionID: "counter-rule", Version: 1}); err != nil {
				t.Fatal(err)
			}
			f.ingestShadow(t, "durable-source", 99000, 7)
			f.db.Close()
			killHistoryChild(t, f.path, mode, "")
			reopenHistoryFixture(t, f)
			f.deliverEngine(t, true)
			f.deliverStrategies(t)
			startHistoryWorker(t, f)
			run, err := f.db.AnalysisRun(ctx, "shadow:crash-candidate:1:1")
			if err != nil {
				t.Fatal(err)
			}
			waitHistoryTask(t, f, run.TaskID, 1)
			candidate, err := f.history.Shadow(ctx, f.principal, "crash-candidate")
			var receipts, runCount int
			if err != nil || candidate.Generation != 1 || candidate.InputCount != 1 {
				t.Fatal(candidate, err)
			}
			if err = f.db.DB.QueryRow("SELECT COUNT(*) FROM sf_shadow_inputs").Scan(&receipts); err != nil || receipts != 1 {
				t.Fatal(receipts, err)
			}
			if err = f.db.DB.QueryRow("SELECT COUNT(*) FROM documents WHERE kind='analysis_run'").Scan(&runCount); err != nil || runCount != 1 {
				t.Fatal(runCount, err)
			}
			formal, err := f.db.Get(ctx, "engine_state", "counter-rule:2:device")
			states, decodeErr := store.Decode[map[string]rulecore.RuntimeState](formal)
			steps, stepErr := f.history.Steps(ctx, f.principal, run.ID, 0, 100)
			if err != nil || decodeErr != nil || stepErr != nil || states["counter"].Count != 7 || len(steps.Items) != 1 || fmt.Sprint(steps.Items[0].Lanes[0].Evaluation.Values["counter"]) != "8" {
				t.Fatal(states, steps, err, decodeErr, stepErr)
			}
			t.Logf("actual SIGKILL at %s; durable outbox redelivery captured once; generation=1 receipts=1 runs=1 formal=7 candidate=8 snapshot=%s", mode, run.SnapshotSHA256)
		})
	}
}

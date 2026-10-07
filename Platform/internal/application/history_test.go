package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/tasks"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

type historyFixture struct {
	history   *History
	db        *store.Store
	engine    *engine.Service
	principal identity.Principal
	base      int64
	path      string
}

func newHistoryFixture(t *testing.T, dsn string) *historyFixture {
	t.Helper()
	if dsn == "" {
		dsn = filepath.Join(t.TempDir(), "history.db")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, dsn, "history-test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.DB.SetMaxOpenConns(3)
	if db.Driver != "pgx" {
		db.DB.SetMaxOpenConns(1)
	}
	db.DB.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	base := time.Now().UnixMilli() - 100000
	db.Now = func() time.Time { return time.UnixMilli(base) }
	for _, entity := range []model.Entity{{ID: "factory", Kind: "asset"}, {ID: "line-a", Kind: "asset", ParentID: "factory"}, {ID: "line-b", Kind: "asset", ParentID: "factory"}, {ID: "other", Kind: "asset"}, {ID: "device", Kind: "device", ParentID: "line-a", SamplingMS: 1000}, {ID: "forbidden", Kind: "device", ParentID: "other"}} {
		if _, err = db.Put(ctx, "entity", entity.ID, 0, entity); err != nil {
			t.Fatal(err)
		}
	}
	eng := &engine.Service{Store: db}
	auth := &identity.Manager{Store: db}
	principal := identity.Principal{User: model.User{ID: "history-engineer", Active: true, Roles: []string{"admin"}, Resources: []string{"*"}}, Actor: model.Actor{UserID: "history-engineer", Source: "test"}}
	hist := &History{Store: db, Identity: auth, Engine: eng}
	eng.ObserveAnalysis = hist.Observe
	db.Now = func() time.Time { return time.UnixMilli(base + 100000) }
	return &historyFixture{history: hist, db: db, engine: eng, principal: principal, base: base, path: dsn}
}
func (f *historyFixture) publish(t *testing.T, d model.Definition) {
	t.Helper()
	if _, err := f.db.Put(context.Background(), "definition", d.ID, d.Version-1, d); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.PrepareDefinition(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}
func (f *historyFixture) rule(version int64, code string) model.Definition {
	return model.Definition{ID: "counter-rule", Name: "Historical counter", Kind: "analysis", SchemaVersion: model.ContractVersion, GroupID: "factory", Status: "published", Version: version, EffectiveMS: f.base + version*1000, Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"count"}}, Nodes: []model.Node{{ID: "value", Type: "expression", Params: map[string]any{"code": code}}, {ID: "counter", Type: "counter", Params: map[string]any{"mode": "delta"}}}, Connections: []model.Connection{{From: "value", To: "counter"}}, Outputs: []model.Output{{NodeID: "counter", Key: "total", Type: "number", Unit: "piece"}}}
}
func (f *historyFixture) point(id string, at int64, value any) model.Observation {
	return model.Observation{ID: id, MessageID: "message:" + id, SourceID: "sensor-gateway", SourceSequence: 1, DeviceID: "device", Key: "count", Value: value, ObservedMS: f.base + at, ReceivedMS: f.base + 100000, Quality: "GOOD", Unit: "piece", TimeSource: "device", Revision: 1, EntityRevision: 1, AssetVersion: 1}
}
func historySideEffectHash(t *testing.T, db *store.Store) string {
	t.Helper()
	items := []any{}
	for _, kind := range []string{"engine_state", "recompute_state", "active_alarm", "alarm", "execution", "active_alarm", "revision"} {
		docs, err := db.List(context.Background(), kind)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, docs)
	}
	var outbox, observations int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM outbox").Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM observations").Scan(&observations); err != nil {
		t.Fatal(err)
	}
	return store.Hash(append(items, outbox, observations))
}

func TestHistoryReplayCrossVersionAssetsAndImmutableSnapshot(t *testing.T) {
	historyReplayCheck(t, newHistoryFixture(t, ""))
}
func TestPostgresHistoryReplayCrossVersionAssetsAndImmutableSnapshot(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "analysis")
	historyReplayCheck(t, newHistoryFixture(t, dsn))
}
func historyReplayCheck(t *testing.T, f *historyFixture) {
	ctx := context.Background()
	first := f.rule(1, "value+1")
	first.Selector.AssetID = "line-a"
	f.publish(t, first)
	second := f.rule(2, "value+2")
	second.EffectiveMS = f.base + 3000
	second.Selector.AssetID = "line-b"
	f.publish(t, second)
	f.db.Now = func() time.Time { return time.UnixMilli(f.base + 3000) }
	if _, err := f.db.Put(ctx, "entity", "device", 1, model.Entity{ID: "device", Kind: "device", ParentID: "line-b", SamplingMS: 2000}); err != nil {
		t.Fatal(err)
	}
	f.db.Now = func() time.Time { return time.UnixMilli(f.base + 100000) }
	a, b := f.point("before", 2000, json.Number("9007199254740993")), f.point("after", 3500, 4)
	b.Late = true
	b.Quality = "UNCERTAIN"
	b.QualityReason = "late source measurement"
	b.AssetVersion = 2
	before := historySideEffectHash(t, f.db)
	statistics := rulecore.SnapshotStatistics()
	run, err := f.history.Create(ctx, f.principal, historymodel.Request{ID: "cross-version", Kind: "replay", DefinitionID: first.ID, FromMS: f.base + 1500, ToMS: f.base + 4000, Points: []model.Observation{b, a}, History: []model.Observation{}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.history.Snapshot(ctx, f.principal, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Rules) != 2 || len(snapshot.Points) != 2 || snapshot.Points[0].ID != a.ID || snapshot.Points[1].QualityReason != b.QualityReason || snapshot.Points[1].Revision != 1 {
		t.Fatalf("snapshot %+v", snapshot)
	}
	if err = f.history.Execute(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	steps, err := f.history.Steps(ctx, f.principal, run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps.Items) != 2 || steps.Items[0].Lanes[0].DefinitionVersion != 1 || steps.Items[1].Lanes[0].DefinitionVersion != 2 {
		t.Fatalf("cross-version steps %+v", steps)
	}
	if fmt.Sprint(steps.Items[0].Lanes[0].Evaluation.Values["value"]) != "9007199254740994" || steps.Items[0].Lanes[0].Evaluation.EntityID != "line-a" || steps.Items[1].Lanes[0].Evaluation.EntityID != "line-b" || steps.Items[1].Lanes[0].Evaluation.AssetVersions[0].Version != 2 {
		t.Fatalf("versioned results %+v", steps)
	}
	after := rulecore.SnapshotStatistics()
	if after.Compilations != statistics.Compilations || after.Executions-statistics.Executions != 2 {
		t.Fatal("analysis recompiled or repeated", statistics, after)
	}
	if afterHash := historySideEffectHash(t, f.db); before != afterHash {
		t.Fatal("analysis changed formal state/projections", before, afterHash)
	}
	if err = f.history.Execute(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := f.history.Snapshot(ctx, f.principal, run.ID)
	if again.SHA256 != snapshot.SHA256 {
		t.Fatal("snapshot changed after repeat")
	}
	t.Logf("replay snapshot=%s input=%s outputs=2 versions=1,2 exact_value=9007199254740994 assets=1,2 formal_hash=%s", snapshot.SHA256, snapshot.InputSHA256, before)
}

func TestHistoryCompareSameInputClockPreciseWindowAndDifferences(t *testing.T) {
	f := newHistoryFixture(t, "")
	ctx := context.Background()
	d := f.rule(1, "value")
	d.Selector.WindowMS = 1000
	d.Nodes = []model.Node{{ID: "sum", Type: "aggregate", Params: map[string]any{"function": "sum"}}, {ID: "value", Type: "expression", Params: map[string]any{"code": "value"}}}
	d.Connections = []model.Connection{{From: "sum", To: "value"}}
	d.Outputs = []model.Output{{NodeID: "value", Key: "reading", Type: "number"}}
	f.publish(t, d)
	d.Version = 2
	d.EffectiveMS = f.base + 2000
	d.Nodes[1].Params = map[string]any{"code": "value+1"}
	f.publish(t, d)
	d.Version = 3
	d.EffectiveMS = f.base + 3000
	f.publish(t, d)
	at := f.base + 9000
	fresh := int64(5000)
	points := []model.Observation{f.point("third", 4000, 5), f.point("first", 3000, json.Number("9007199254740993")), f.point("second", 3500, 2)}
	points[1].SourceSequence = 2
	request := historymodel.Request{Kind: "compare", DefinitionID: d.ID, LeftVersion: 1, RightVersion: 2, FromMS: f.base + 3000, ToMS: f.base + 4000, Points: points, History: []model.Observation{f.point("past", 2501, 3)}, InitialState: historymodel.State{}, Clock: historymodel.Clock{AtMS: &at, StepMS: 10, FreshnessMS: &fresh}}
	run, err := f.history.Create(ctx, f.principal, request)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.history.Execute(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	steps, err := f.history.Steps(ctx, f.principal, run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"9007199254740996", "9007199254740998", "7"}
	for i, step := range steps.Items {
		if !step.Difference.Changed || !step.Difference.Values || fmt.Sprint(step.Lanes[0].Evaluation.Values["value"]) != want[i] || step.ClockMS != at+int64(i)*10 || step.Lanes[0].PlanID == step.Lanes[1].PlanID {
			t.Fatalf("comparison %+v", step)
		}
	}
	request.ID = "unchanged"
	request.LeftVersion = 2
	request.RightVersion = 3
	same, err := f.history.Create(ctx, f.principal, request)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.history.Execute(ctx, same.ID); err != nil {
		t.Fatal(err)
	}
	sameSteps, err := f.history.Steps(ctx, f.principal, same.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range sameSteps.Items {
		if step.Difference.Changed {
			t.Fatalf("equal behavior differs: %+v", step.Difference)
		}
	}
	firstSnapshot, _ := f.history.Snapshot(ctx, f.principal, run.ID)
	sameSnapshot, _ := f.history.Snapshot(ctx, f.principal, same.ID)
	if firstSnapshot.InputSHA256 != sameSnapshot.InputSHA256 {
		t.Fatal("identical conditions changed their input identity")
	}
	t.Logf("comparison changed=3 unchanged=3 shared_input=%s clock_start=%d clock_step=10 exact_window=%v", firstSnapshot.InputSHA256, at, want)
}

type interruptHistoryRepository struct {
	HistoryRepository
	failAt      int
	afterCommit bool
}

func (r *interruptHistoryRepository) CommitAnalysisStep(ctx context.Context, run historymodel.Run, step historymodel.Step, state map[string]historymodel.State) (historymodel.Run, error) {
	if step.InputIndex == r.failAt && !r.afterCommit {
		return historymodel.Run{}, errors.New("injected history storage failure")
	}
	result, err := r.HistoryRepository.CommitAnalysisStep(ctx, run, step, state)
	if err == nil && step.InputIndex == r.failAt && r.afterCommit {
		return result, errors.New("injected failure after committed checkpoint")
	}
	return result, err
}

func TestHistoryCancellationRetryFreezesInputAndResumesCheckpoint(t *testing.T) {
	f := newHistoryFixture(t, "")
	ctx := context.Background()
	f.publish(t, f.rule(1, "value"))
	a, b, c := f.point("one", 2000, 1), f.point("two", 3000, 2), f.point("three", 4000, 3)
	if _, err := f.db.Ingest(ctx, store.IngestBatch{MessageID: "batch", SourceID: "sensor-gateway", Points: []model.Observation{a, b, c}}); err != nil {
		t.Fatal(err)
	}
	run, err := f.history.Create(ctx, f.principal, historymodel.Request{ID: "recoverable", Kind: "replay", DefinitionID: "counter-rule", FromMS: f.base + 2000, ToMS: f.base + 4000, DeviceIDs: []string{"device"}, Keys: []string{"count"}})
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := f.history.Snapshot(ctx, f.principal, run.ID)
	f.history.Store = &interruptHistoryRepository{HistoryRepository: f.db, failAt: 0, afterCommit: true}
	if err = f.history.Execute(ctx, run.ID); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatal(err)
	}
	failed, _ := f.db.AnalysisRun(ctx, run.ID)
	if failed.Status != "failed" || failed.Cursor != 1 {
		t.Fatal(failed)
	}
	task, _ := f.db.Task(ctx, run.TaskID)
	cancelled, err := f.db.ChangeTask(ctx, task.ID, task.Version, "cancel", f.principal.Actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.BusinessState != "cancelled" {
		t.Fatal(cancelled)
	}
	if err = f.history.Execute(ctx, run.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = f.db.Ingest(ctx, store.IngestBatch{MessageID: "later-input", SourceID: "sensor-gateway", Points: []model.Observation{f.point("additional", 2500, 1000)}}); err != nil {
		t.Fatal(err)
	}
	retry, err := f.db.ChangeTask(ctx, cancelled.ID, cancelled.Version, "retry", f.principal.Actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != task.ID || retry.RiverID != task.RiverID {
		t.Fatal("retry replaced durable task", task, retry)
	}
	f.history.Store = f.db
	statistics := rulecore.SnapshotStatistics()
	if err = f.history.Execute(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	after := rulecore.SnapshotStatistics()
	if after.Executions-statistics.Executions != 2 {
		t.Fatal("committed prefix executed again", statistics, after)
	}
	complete, _ := f.db.AnalysisRun(ctx, run.ID)
	snapshot, _ := f.history.Snapshot(ctx, f.principal, run.ID)
	if complete.Cursor != 3 || complete.Status != "completed" || snapshot.SHA256 != initial.SHA256 || len(snapshot.Points) != 3 || complete.FinalState["version:1"]["device"]["counter"].Count != 6 {
		t.Fatal(complete, snapshot)
	}
	t.Logf("retry task=%s river=%d cursor=1->3 stable_snapshot=%s suffix_executions=2", task.ID, task.RiverID, snapshot.SHA256)
}

func TestHistoryAuthorizationBudgetAndSnapshotCorruption(t *testing.T) {
	f := newHistoryFixture(t, "")
	ctx := context.Background()
	f.publish(t, f.rule(1, "value"))
	request := historymodel.Request{ID: "permission-run", Kind: "replay", DefinitionID: "counter-rule", FromMS: f.base + 2000, ToMS: f.base + 3000, Points: []model.Observation{f.point("input", 2000, 1)}, History: []model.Observation{}}
	run, err := f.history.Create(ctx, f.principal, request)
	if err != nil {
		t.Fatal(err)
	}
	limited := f.principal
	limited.User.Roles = []string{"engineer"}
	limited.User.Resources = []string{"other"}
	if _, err = f.history.Get(ctx, limited, run.ID); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("run query authorization", err)
	}
	if _, err = f.history.Snapshot(ctx, limited, run.ID); !errors.Is(err, identity.ErrDenied) {
		t.Fatal(err)
	}
	if _, err = f.history.Steps(ctx, limited, run.ID, 0, 100); !errors.Is(err, identity.ErrDenied) {
		t.Fatal(err)
	}
	request.ID = "forbidden-history"
	private := f.point("private", 1000, 1)
	private.DeviceID = "forbidden"
	request.History = []model.Observation{private}
	limited.User.Resources = []string{"factory"}
	if _, err = f.history.Create(ctx, limited, request); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("history scope accepted", err)
	}
	request.History = []model.Observation{}
	request.Points = make([]model.Observation, historymodel.MaxPoints+1)
	if _, err = f.history.Create(ctx, f.principal, request); err == nil {
		t.Fatal("input budget ignored")
	}
	if _, err = f.db.DB.Exec("UPDATE sf_analysis_snapshots SET data=replace(data,'sensor-gateway','modified-source') WHERE id=$1", run.SnapshotID); err != nil {
		t.Fatal(err)
	}
	if err = f.history.Execute(ctx, run.ID); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatal("corrupt immutable input executed", err)
	}
	t.Log("all result routes reauthorize; hidden history rejected; 10001 inputs rejected; edited persisted snapshot refused")
}

func TestHistoryRiverWorkerExecutesDurableAnalysis(t *testing.T) {
	historyRiverCheck(t, newHistoryFixture(t, ""))
}
func TestPostgresHistoryRiverWorkerExecutesDurableAnalysis(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "history_river")
	historyRiverCheck(t, newHistoryFixture(t, dsn))
}
func historyRiverCheck(t *testing.T, f *historyFixture) {
	ctx := context.Background()
	f.publish(t, f.rule(1, "value"))
	run, err := f.history.Create(ctx, f.principal, historymodel.Request{ID: "river-analysis", Kind: "replay", DefinitionID: "counter-rule", FromMS: f.base + 2000, ToMS: f.base + 3000, Points: []model.Observation{f.point("input", 2000, 7)}, History: []model.Observation{}})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := tasks.New(f.db, tasks.Handlers{Analysis: f.history.Execute})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := worker.Stop(stop); err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task, err := f.db.Task(ctx, run.TaskID)
		if err == nil && task.State == "completed" {
			if task.Queue != "history_analysis" || task.BusinessState != "completed" || task.Progress != 1 {
				t.Fatal(task)
			}
			t.Logf("River task=%s river=%d attempt=%d queue=%s snapshot=%s", task.ID, task.RiverID, task.Attempt, task.Queue, run.SnapshotSHA256)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	task, _ := f.db.Task(ctx, run.TaskID)
	t.Fatal("analysis worker did not complete", task)
}

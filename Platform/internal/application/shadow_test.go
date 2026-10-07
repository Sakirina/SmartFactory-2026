package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func TestShadowProductionDerivedRevisionComparedWithCommittedRecompute(t *testing.T) {
	shadowDerivedRevisionCheck(t, newHistoryFixture(t, ""))
}
func TestPostgresShadowProductionDerivedRevisionComparedWithCommittedRecompute(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "shadow_revision")
	shadowDerivedRevisionCheck(t, newHistoryFixture(t, dsn))
}
func shadowDerivedRevisionCheck(t *testing.T, f *historyFixture) {
	ctx := context.Background()
	upstream := model.Definition{ID: "a-source", Name: "Source window", Kind: "analysis", SchemaVersion: model.ContractVersion, GroupID: "factory", Status: "published", Version: 1, EffectiveMS: f.base + 1000, Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"count"}, WindowMS: 60000}, Nodes: []model.Node{{ID: "sum", Type: "aggregate", Params: map[string]any{"function": "sum"}}}, Outputs: []model.Output{{NodeID: "sum", Key: "reading", Type: "number"}}}
	f.publish(t, upstream)
	downstream := f.rule(1, "value+1")
	downstream.Selector.Keys = []string{"a-source.reading"}
	downstream.Dependencies = []string{upstream.ID}
	downstream.Nodes = downstream.Nodes[:1]
	downstream.Connections = nil
	downstream.Outputs = []model.Output{{NodeID: "value", Key: "result", Type: "number"}}
	f.publish(t, downstream)
	downstream.Version = 2
	downstream.EffectiveMS = f.base + 2000
	downstream.Nodes[0].Params = map[string]any{"code": "value"}
	f.publish(t, downstream)
	if _, err := f.history.EnableShadow(ctx, f.principal, historymodel.ShadowRequest{ID: "derived", DefinitionID: downstream.ID, Version: 1}); err != nil {
		t.Fatal(err)
	}
	f.ingestShadow(t, "first-source", 98000, 10)
	f.deliverEngine(t, true)
	f.deliverEngine(t, true)
	f.deliverEngine(t, true)
	f.ingestShadow(t, "second-source", 100000, 30)
	f.deliverEngine(t, true)
	f.deliverEngine(t, true)
	f.deliverEngine(t, true)
	candidate, err := f.history.Shadow(ctx, f.principal, "derived")
	if err != nil || candidate.Generation != 2 {
		t.Fatal(candidate, err)
	}
	f.executeShadowGeneration(t, "derived", 1, 1)
	f.executeShadowGeneration(t, "derived", 1, 2)
	f.db.Now = func() time.Time { return time.UnixMilli(f.base + 100100) }
	f.ingestShadow(t, "late-source", 99000, 20)
	f.deliverEngine(t, true)
	jobDoc, err := f.db.Get(ctx, "job", "backfill:device")
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Decode[model.Job](jobDoc)
	if err != nil {
		t.Fatal(err)
	}
	job.Version = jobDoc.Version
	if err = f.engine.Recompute(ctx, job); err != nil {
		t.Fatal(err)
	}
	during, err := f.history.Shadow(ctx, f.principal, "derived")
	if err != nil || during.Generation != 2 {
		t.Fatal("internal recompute frames were captured", during, err)
	}
	for i := 0; i < 4; i++ {
		f.deliverEngine(t, true)
		f.deliverStrategies(t)
	}
	candidate, err = f.history.Shadow(ctx, f.principal, "derived")
	if err != nil || candidate.Generation != 4 {
		t.Fatalf("derived revisions did not propagate: %+v %v", candidate, err)
	}
	f.executeShadowGeneration(t, "derived", 1, 3)
	last := f.executeShadowGeneration(t, "derived", 1, 4)
	steps, err := f.history.Steps(ctx, f.principal, last.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps.Items) != 3 {
		t.Fatal("correction did not replay effective revision sequence", steps)
	}
	revised := steps.Items[2]
	if revised.Point.Revision != 2 || !revised.Point.Late || revised.FormalStatus != "historical_recompute_projection" || revised.FormalEvaluation == nil || revised.FormalEvaluation.RunID == "" || fmt.Sprint(revised.FormalEvaluation.Values["value"]) != "60" || fmt.Sprint(revised.Lanes[0].Evaluation.Values["value"]) != "61" || len(revised.FormalOutputs) != 1 || revised.FormalOutputs[0].Revision != 2 || fmt.Sprint(revised.FormalOutputs[0].Value) != "60" {
		t.Fatalf("revised formal comparison %+v", revised)
	}
	var receipts int
	if err = f.db.DB.QueryRow("SELECT COUNT(*) FROM sf_shadow_inputs WHERE candidate_id='derived'").Scan(&receipts); err != nil || receipts != 4 {
		t.Fatal(receipts, err)
	}
	f.deliverEngine(t, true)
	again, _ := f.history.Shadow(ctx, f.principal, "derived")
	if again.Generation != 4 {
		t.Fatal("revision delivery duplicated", again)
	}
	t.Logf("actual raw late input -> Recompute %s -> derived revision2 outbox -> shadow generation4: effective_inputs=3 captured_revisions=4 candidate=61 formal=60 formal_output_revision=2 formal_run=%s", revised.FormalEvaluation.RunID, revised.FormalEvaluation.RunID)
}

func TestShadowCalculationFailurePreservesFormalStateAndIntentIsolation(t *testing.T) {
	f := newHistoryFixture(t, "")
	ctx := context.Background()
	candidate := f.rule(1, "value")
	candidate.Nodes = []model.Node{{ID: "missing", Type: "input", Params: map[string]any{"key": "missing_field"}}}
	candidate.Connections = nil
	candidate.Outputs = []model.Output{{NodeID: "missing", Key: "total", Type: "number"}}
	f.publish(t, candidate)
	f.publish(t, f.rule(2, "value"))
	if _, err := f.history.EnableShadow(ctx, f.principal, historymodel.ShadowRequest{ID: "failed-candidate", DefinitionID: "counter-rule", Version: 1}); err != nil {
		t.Fatal(err)
	}
	f.ingestShadow(t, "valid-formal", 99000, 5)
	f.deliverEngine(t, true)
	before := historySideEffectHash(t, f.db)
	if err := f.history.Execute(ctx, "shadow:failed-candidate:1:1"); err == nil {
		t.Fatal("missing candidate input was accepted")
	}
	failed, err := f.db.AnalysisRun(ctx, "shadow:failed-candidate:1:1")
	if err != nil || failed.Status != "failed" || failed.Cursor != 0 {
		t.Fatal(failed, err)
	}
	if after := historySideEffectHash(t, f.db); after != before {
		t.Fatal("failed candidate changed formal state", before, after)
	}
	f.ingestShadow(t, "formal-continues", 99500, 7)
	f.deliverEngine(t, true)
	formal, err := f.db.Get(ctx, "engine_state", "counter-rule:2:device")
	if err != nil {
		t.Fatal(err)
	}
	var states map[string]rulecore.RuntimeState
	if err = store.DecodeJSON(formal.Data, &states); err != nil || states["counter"].Count != 12 {
		t.Fatal(states, err)
	}
	if err = f.history.Execute(ctx, "shadow:failed-candidate:1:2"); !errors.Is(err, historymodel.ErrParentPending) {
		t.Fatal("failed parent was ignored", err)
	}
	// A strategy candidate produces a persisted action intent while the active
	// formal version keeps its own false trigger and creates no device work.
	strategy := model.Definition{ID: "intent-rule", Name: "Candidate intention", Kind: "strategy", SchemaVersion: model.ContractVersion, GroupID: "factory", Status: "published", Version: 1, EffectiveMS: f.base + 1000, Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"intent"}}, Nodes: []model.Node{{ID: "condition", Type: "expression", Params: map[string]any{"code": "value>0"}}, {ID: "action", Type: "action"}}, Connections: []model.Connection{{From: "condition", To: "action"}}, Policy: model.Policy{Steps: []model.Step{{ID: "candidate-step", EdgeID: "history-test", DeviceID: "device", Action: "fixture-action", Idempotent: true}}}}
	f.publish(t, strategy)
	strategy.Version = 2
	strategy.EffectiveMS = f.base + 2000
	strategy.Nodes[0].Params = map[string]any{"code": "false"}
	f.publish(t, strategy)
	if _, err = f.history.EnableShadow(ctx, f.principal, historymodel.ShadowRequest{ID: "intent-candidate", DefinitionID: strategy.ID, Version: 1}); err != nil {
		t.Fatal(err)
	}
	point := f.point("intent-input", 99800, 1)
	point.Key = "intent"
	if _, err = f.db.Ingest(ctx, store.IngestBatch{MessageID: "intent-input", SourceID: point.SourceID, Points: []model.Observation{point}}); err != nil {
		t.Fatal(err)
	}
	f.engine.ControlEnabled = true
	f.deliverStrategies(t)
	formalBefore := historySideEffectHash(t, f.db)
	run := f.executeShadowGeneration(t, "intent-candidate", 1, 1)
	results, err := f.history.Steps(ctx, f.principal, run.ID, 0, 100)
	if err != nil || len(results.Items) != 1 || len(results.Items[0].Lanes[0].ActionIntents) != 1 || !results.Items[0].Lanes[0].Evaluation.Trigger {
		t.Fatal(results, err)
	}
	if after := historySideEffectHash(t, f.db); after != formalBefore {
		t.Fatal("candidate action changed formal records")
	}
	var actions int
	if err = f.db.DB.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind IN ('strategy_trigger','edge_downlink')").Scan(&actions); err != nil || actions != 0 {
		t.Fatal(actions, err)
	}
	t.Log("candidate failure preserved formal counter, next formal input advanced to12; failed parent waits; candidate action intent=1 formal trigger deliveries=0")
}

func (f *historyFixture) ingestShadow(t *testing.T, id string, at, value int64) model.Observation {
	t.Helper()
	p := f.point(id, at, value)
	p.SourceSequence = uint64(at)
	_, err := f.db.Ingest(context.Background(), store.IngestBatch{MessageID: "message:" + id, SourceID: p.SourceID, Points: []model.Observation{p}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The same persistent delivery contents and calculation entry points used by
// app.startWorkers are exercised here with controlled acknowledgement timing.
func (f *historyFixture) deliverEngine(t *testing.T, acknowledge bool) []store.Delivery {
	t.Helper()
	ctx := context.Background()
	deliveries, err := f.db.Deliveries(ctx, "engine", 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range deliveries {
		var p model.Observation
		if err = store.DecodeJSON(delivery.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if err = f.engine.ProcessCalculations(ctx, p, p.Late); err != nil {
			t.Fatal(err)
		}
		if acknowledge {
			if err = f.db.DeliveryDone(ctx, delivery.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	return deliveries
}
func (f *historyFixture) deliverStrategies(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	deliveries, err := f.db.Deliveries(ctx, "strategy", 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range deliveries {
		var p model.Observation
		if err = store.DecodeJSON(delivery.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if err = f.engine.ProcessStrategies(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err = f.db.DeliveryDone(ctx, delivery.ID); err != nil {
			t.Fatal(err)
		}
	}
}
func (f *historyFixture) executeShadowGeneration(t *testing.T, id string, epoch, generation int64) historymodel.Run {
	t.Helper()
	runID := fmt.Sprintf("shadow:%s:%d:%d", id, epoch, generation)
	if err := f.history.Execute(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	run, err := f.db.AnalysisRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestShadowProductionDeliveriesDeduplicateContinueAndWaitForParent(t *testing.T) {
	shadowDeliveryCheck(t, newHistoryFixture(t, ""))
}
func TestPostgresShadowProductionDeliveriesDeduplicateContinueAndWaitForParent(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "shadow_delivery")
	shadowDeliveryCheck(t, newHistoryFixture(t, dsn))
}
func shadowDeliveryCheck(t *testing.T, f *historyFixture) {
	ctx := context.Background()
	f.publish(t, f.rule(1, "value+1"))
	f.publish(t, f.rule(2, "value"))
	candidate, err := f.history.EnableShadow(ctx, f.principal, historymodel.ShadowRequest{ID: "candidate", DefinitionID: "counter-rule", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	statistics := rulecore.SnapshotStatistics()
	for i := int64(0); i < 20; i++ {
		f.ingestShadow(t, fmt.Sprintf("live-%d", i), 99000+i, 1)
		f.deliverEngine(t, true)
		f.deliverStrategies(t)
	}
	current, err := f.history.Shadow(ctx, f.principal, candidate.ID)
	if err != nil || current.Generation != 20 || current.InputCount != 20 || len(current.Context) != 1 {
		t.Fatal(current, err)
	}
	if err = f.history.Execute(ctx, "shadow:candidate:1:2"); !errors.Is(err, historymodel.ErrParentPending) {
		t.Fatal("second generation failed to wait", err)
	}
	waiting, _ := f.db.AnalysisRun(ctx, "shadow:candidate:1:2")
	if waiting.Status != "waiting_parent" || waiting.InitialStateResolved {
		t.Fatal(waiting)
	}
	againErr := f.history.Execute(ctx, waiting.ID)
	sameWait, _ := f.db.AnalysisRun(ctx, waiting.ID)
	if !errors.Is(againErr, historymodel.ErrParentPending) || sameWait.Version != waiting.Version {
		t.Fatal("unchanged wait rewrote the run", waiting, sameWait, againErr)
	}
	var last historymodel.Run
	for i := int64(1); i <= 20; i++ {
		last = f.executeShadowGeneration(t, "candidate", 1, i)
		if last.Total != 1 {
			t.Fatal("normal input replayed its prefix", last)
		}
	}
	after := rulecore.SnapshotStatistics()
	if after.Executions-statistics.Executions != 40 {
		t.Fatal("expected 20 formal + 20 shadow executions", statistics, after)
	}
	snapshot, err := f.history.Snapshot(ctx, f.principal, last.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !last.InitialStateResolved || snapshot.InitialState["device"]["counter"].Count != 38 || last.FinalState["candidate"]["device"]["counter"].Count != 40 || last.CapturedSnapshotID == last.SnapshotID {
		t.Fatal("shadow initial state was not fixed", last, snapshot)
	}
	steps, err := f.history.Steps(ctx, f.principal, last.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps.Items) != 1 || steps.Items[0].FormalStatus != "realtime_evaluation" || !steps.Items[0].FormalDifference.Changed || steps.Items[0].FormalEvaluation.States["counter"].Count != 20 || len(steps.Items[0].FormalOutputs) != 1 || fmt.Sprint(steps.Items[0].FormalOutputs[0].Value) != "20" {
		t.Fatalf("formal relationship %+v", steps)
	}
	var count, inputs int
	if err = f.db.DB.QueryRow("SELECT COUNT(*) FROM sf_analysis_steps").Scan(&count); err != nil || count != 20 {
		t.Fatal(count, err)
	}
	if err = f.db.DB.QueryRow("SELECT COUNT(*) FROM sf_shadow_inputs").Scan(&inputs); err != nil || inputs != 20 {
		t.Fatal(inputs, err)
	}
	length := "length(CAST(data AS BLOB))"
	if f.db.Driver == "pgx" {
		length = "octet_length(data)"
	}
	for _, item := range []struct{ name, from string }{
		{"snapshots", "sf_analysis_snapshots"},
		{"steps", "sf_analysis_steps"},
		{"captured_inputs", "sf_shadow_inputs"},
		{"candidate_current", "documents WHERE kind='shadow_candidate'"},
		{"candidate_versions", "document_versions WHERE kind='shadow_candidate'"},
		{"runs_current", "documents WHERE kind='analysis_run'"},
		{"runs_versions", "document_versions WHERE kind='analysis_run'"},
	} {
		var rows, size int64
		if err = f.db.DB.QueryRow("SELECT COUNT(*),COALESCE(SUM("+length+"),0) FROM "+item.from).Scan(&rows, &size); err != nil {
			t.Fatal(err)
		}
		t.Logf("20-input persisted payload backend=%s category=%s rows=%d utf8_bytes=%d", f.db.Driver, item.name, rows, size)
	}
	rows, err := f.db.DB.Query("SELECT data FROM sf_analysis_snapshots")
	if err != nil {
		t.Fatal(err)
	}
	var maxPoints, maxHistory int
	for rows.Next() {
		var raw string
		var saved historymodel.Snapshot
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err = store.DecodeJSON([]byte(raw), &saved); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		maxPoints, maxHistory = max(maxPoints, len(saved.Points)), max(maxHistory, len(saved.History))
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if maxPoints != 1 || maxHistory != 1 {
		t.Fatal("normal snapshots retained a growing prefix", maxPoints, maxHistory)
	}
	t.Logf("20-input retained context backend=%s rule_window_ms=0 latest_per_device_key=1 candidate_context_points=%d snapshot_max_inputs=%d snapshot_max_history=%d; payload bytes exclude database indexes, task/audit envelopes and WAL", f.db.Driver, len(current.Context), maxPoints, maxHistory)
	current, _ = f.history.Shadow(ctx, f.principal, candidate.ID)
	stopped, err := f.history.DisableShadow(ctx, f.principal, candidate.ID, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	f.ingestShadow(t, "after-stop", 99900, 1)
	f.deliverEngine(t, true)
	f.deliverStrategies(t)
	current, _ = f.history.Shadow(ctx, f.principal, candidate.ID)
	if current.Generation != 20 || current.Status != "stopped" {
		t.Fatal(current)
	}
	replaced, err := f.history.EnableShadow(ctx, f.principal, historymodel.ShadowRequest{ID: candidate.ID, DefinitionID: "counter-rule", Version: 2, ExpectedVersion: stopped.Version})
	if err != nil || replaced.Epoch != 2 {
		t.Fatal(replaced, err)
	}
	f.ingestShadow(t, "after-replace", 99950, 1)
	f.deliverEngine(t, true)
	replacement := f.executeShadowGeneration(t, candidate.ID, 2, 1)
	if replacement.FinalState["candidate"]["device"]["counter"].Count != 1 {
		t.Fatal(replacement)
	}
	t.Logf("20 inputs: actual executions=%d, stored steps=%d, receipts=%d; normal run total=1; second generation waited without rewriting; final candidate=40 formal=20; stopped generation=20; replacement epoch=2 candidate=1", after.Executions-statistics.Executions, count, inputs)
}

type captureFaultRepository struct {
	HistoryRepository
	before func(historymodel.Candidate, model.Observation) error
	after  bool
}

func (r *captureFaultRepository) CaptureShadow(ctx context.Context, c historymodel.Candidate, p model.Observation, run historymodel.Run, snapshot historymodel.Snapshot) error {
	if r.before != nil {
		if err := r.before(c, p); err != nil {
			return err
		}
	}
	if err := r.HistoryRepository.CaptureShadow(ctx, c, p, run, snapshot); err != nil {
		return err
	}
	if r.after {
		return errors.New("injected process interruption after shadow capture")
	}
	return nil
}

func TestShadowDeliveryInterruptionBeforeCaptureAfterCaptureAndBeforeAcknowledgement(t *testing.T) {
	for _, stage := range []string{"before_capture", "after_capture", "before_ack"} {
		t.Run(stage, func(t *testing.T) {
			f := newHistoryFixture(t, "")
			ctx := context.Background()
			f.publish(t, f.rule(1, "value"))
			if _, err := f.history.EnableShadow(ctx, f.principal, historymodel.ShadowRequest{ID: "durable", DefinitionID: "counter-rule", Version: 1}); err != nil {
				t.Fatal(err)
			}
			f.ingestShadow(t, "original", 99000, 7)
			deliveries, err := f.db.Deliveries(ctx, "engine", 10)
			if err != nil || len(deliveries) != 1 {
				t.Fatal(deliveries, err)
			}
			var p model.Observation
			if err = store.DecodeJSON(deliveries[0].Payload, &p); err != nil {
				t.Fatal(err)
			}
			if stage == "before_capture" {
				f.history.Store = &captureFaultRepository{HistoryRepository: f.db, before: func(historymodel.Candidate, model.Observation) error {
					return errors.New("injected process interruption before capture")
				}}
			} else if stage == "after_capture" {
				f.history.Store = &captureFaultRepository{HistoryRepository: f.db, after: true}
			}
			first := f.engine.ProcessCalculations(ctx, p, p.Late)
			if stage != "before_ack" && first == nil {
				t.Fatal("fault did not interrupt", stage)
			}
			if stage == "before_ack" && first != nil {
				t.Fatal(first)
			}
			var before int
			if err = f.db.DB.QueryRow("SELECT COUNT(*) FROM sf_shadow_inputs").Scan(&before); err != nil {
				t.Fatal(err)
			}
			want := 1
			if stage == "before_capture" {
				want = 0
			}
			if before != want {
				t.Fatal(stage, before)
			}
			f.history.Store = f.db
			f.engine.ObserveAnalysis = f.history.Observe
			if err = f.engine.ProcessCalculations(ctx, p, p.Late); err != nil {
				t.Fatal(err)
			}
			if err = f.engine.ProcessStrategies(ctx, p); err != nil {
				t.Fatal(err)
			}
			if err = f.db.DeliveryDone(ctx, deliveries[0].ID); err != nil {
				t.Fatal(err)
			}
			run := f.executeShadowGeneration(t, "durable", 1, 1)
			var count, inputs int
			if err = f.db.DB.QueryRow("SELECT COUNT(*) FROM sf_tasks WHERE kind='analysis'").Scan(&count); err != nil || count != 1 {
				t.Fatal(count, err)
			}
			if err = f.db.DB.QueryRow("SELECT COUNT(*) FROM sf_shadow_inputs").Scan(&inputs); err != nil || inputs != 1 {
				t.Fatal(inputs, err)
			}
			formal, err := f.db.Get(ctx, "engine_state", "counter-rule:1:device")
			if err != nil {
				t.Fatal(err)
			}
			var state map[string]rulecore.RuntimeState
			if err = store.DecodeJSON(formal.Data, &state); err != nil {
				t.Fatal(err)
			}
			if state["counter"].Count != 7 || run.FinalState["candidate"]["device"]["counter"].Count != 7 {
				t.Fatal(state, run)
			}
			t.Logf("%s: receipt=1 task=%s formal_count=7 candidate_count=7 pending_input_removed=true", stage, run.TaskID)
		})
	}
}

func TestShadowCandidateStopAndReplacementRaceUsesCurrentScope(t *testing.T) {
	for _, action := range []string{"stop", "replace_scope"} {
		t.Run(action, func(t *testing.T) {
			f := newHistoryFixture(t, "")
			ctx := context.Background()
			f.publish(t, f.rule(1, "value"))
			if _, err := f.history.EnableShadow(ctx, f.principal, historymodel.ShadowRequest{ID: "race", DefinitionID: "counter-rule", Version: 1}); err != nil {
				t.Fatal(err)
			}
			once := false
			f.history.Store = &captureFaultRepository{HistoryRepository: f.db, before: func(c historymodel.Candidate, p model.Observation) error {
				if once {
					return nil
				}
				once = true
				direct := *f.history
				direct.Store = f.db
				if action == "stop" {
					_, err := direct.DisableShadow(ctx, f.principal, c.ID, c.Version)
					return err
				}
				_, err := direct.EnableShadow(ctx, f.principal, historymodel.ShadowRequest{ID: c.ID, DefinitionID: c.DefinitionID, Version: 1, ExpectedVersion: c.Version, DeviceIDs: []string{"forbidden"}})
				if err != nil {
					return err
				}
				return store.ErrConflict
			}}
			f.ingestShadow(t, "racing-input", 99000, 1)
			f.deliverEngine(t, true)
			candidate, err := f.history.Shadow(ctx, f.principal, "race")
			if err != nil {
				t.Fatal(err)
			}
			if candidate.Generation != 0 {
				t.Fatal("racing input entered the replacement or stopped candidate", candidate)
			}
			var inputs int
			if err = f.db.DB.QueryRow("SELECT COUNT(*) FROM sf_shadow_inputs").Scan(&inputs); err != nil || inputs != 0 {
				t.Fatal(inputs, err)
			}
			t.Logf("%s: epoch=%d status=%s generation=0 input_receipts=0; formal delivery completed", action, candidate.Epoch, candidate.Status)
		})
	}
}

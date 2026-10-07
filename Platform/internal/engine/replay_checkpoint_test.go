package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func replayFixture(t *testing.T) (*Service, model.Job, []model.Observation) {
	t.Helper()
	s := engineFixture(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour).Truncate(time.Minute)
	s.Store.Now = func() time.Time { return base.Add(30 * time.Minute) }
	parent := definition()
	parent.ID, parent.EffectiveMS, parent.Selector.WindowMS = "parent", base.UnixMilli()-1000, 0
	parent.Nodes[1].Type, parent.Nodes[1].Params = "counter", map[string]any{"mode": "delta"}
	child := definition()
	child.ID, child.EffectiveMS, child.Selector.WindowMS = "child", parent.EffectiveMS, 0
	child.Selector.Keys, child.Dependencies = []string{"parent.mean"}, []string{"parent"}
	child.Nodes[1].Type, child.Nodes[1].Params = "expression", map[string]any{"code": "value*2"}
	for _, definition := range []model.Definition{parent, child} {
		if _, err := s.Store.Put(ctx, "definition", definition.ID, 0, definition); err != nil {
			t.Fatal(err)
		}
	}
	prepareLegacyFixture(t, s)
	initial, _ := json.Marshal(map[string]RuntimeState{"sum": {Count: 10}})
	if _, err := s.Store.DB.Exec("INSERT INTO engine_checkpoints(state_id,bucket_ms,at_ms,data) VALUES($1,$2,$3,$4)", "parent:1:device", (base.UnixMilli()-1)/60000*60000, base.UnixMilli()-1, string(initial)); err != nil {
		t.Fatal(err)
	}
	points := []model.Observation{}
	for i, delta := range []int64{0, 0, 11 * 60000, 21 * 60000} {
		points = append(points, model.Observation{ID: fmt.Sprintf("replay-raw-%d", i), MessageID: fmt.Sprintf("replay-input-%d", i), SourceID: "source", SourceSequence: uint64(i + 1), DeviceID: "device", Key: "temperature", Value: 1 << i, ObservedMS: base.UnixMilli() + delta, ReceivedMS: base.UnixMilli() + delta, Quality: "GOOD", Revision: 1})
	}
	if err := s.Store.Write(ctx, func(tx *store.Tx) error {
		for _, point := range points {
			if err := tx.InsertPoint(point); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	job := model.Job{ID: "recovery", Kind: "recompute", Status: "pending", DeviceID: "device", FromMS: points[0].ObservedMS, ToMS: points[len(points)-1].ObservedMS}
	doc, err := s.Store.Put(ctx, "job", job.ID, 0, job)
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.Decode[model.Job](doc)
	if err != nil {
		t.Fatal(err)
	}
	job.Version = doc.Version
	return s, job, points
}

func TestReplayRestoresParentOutputsAndContinuesCompleteWindows(t *testing.T) {
	s, job, points := replayFixture(t)
	ctx := context.Background()
	job, run, _, err := s.prepareReplay(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	replayCtx := context.WithValue(ctx, replayStartKey{}, run.StartMS)
	replayCtx = context.WithValue(replayCtx, replayFilterKey{}, run.Selected)
	replayCtx = context.WithValue(replayCtx, replayInitialKey{}, job.ReplayID)
	replayCtx = context.WithValue(replayCtx, replayGuardKey{}, replayGuard{JobID: job.ID, RunID: job.ReplayID})
	// End the first process between the committed parent and its child traversal.
	outputs, err := s.processLocked(replayCtx, points[0], true, job.ReplayID)
	if err != nil || len(outputs) != 1 || fmt.Sprint(outputs[0].Value) != "11" {
		t.Fatal(outputs, err)
	}
	if _, err := s.Store.Latest(ctx, "device", "child.mean"); err == nil {
		t.Fatal("child ran before the injected interruption")
	}
	if _, err := s.Store.DB.Exec("UPDATE engine_checkpoints SET data=$1", `{"sum":{"count":1000}}`); err != nil {
		t.Fatal(err)
	}
	firstCursor := run.StartMS + 10*60000 - 1
	trigger := fmt.Sprintf(`CREATE TRIGGER stop_second_window BEFORE UPDATE ON documents WHEN NEW.kind='job' AND json_extract(NEW.data,'$.status')='running' AND json_extract(NEW.data,'$.cursor_ms')>%d BEGIN SELECT RAISE(ABORT,'injected checkpoint failure'); END`, firstCursor)
	if _, err := s.Store.DB.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{Store: s.Store}
	if err := restarted.Recompute(ctx, job); err == nil {
		t.Fatal("window interruption was not injected")
	}
	doc, err := s.Store.Get(ctx, "job", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	interrupted, err := store.Decode[model.Job](doc)
	if err != nil || interrupted.CursorMS != firstCursor || interrupted.Progress <= 0 || interrupted.Progress >= 1 || interrupted.ReplayID != job.ReplayID || interrupted.ReplayStartMS != run.StartMS || interrupted.ReplayEndMS != run.EndMS {
		t.Fatal(interrupted, err)
	}
	if _, err := s.Store.DB.Exec("DROP TRIGGER stop_second_window"); err != nil {
		t.Fatal(err)
	}
	// The retry uses the frozen predecessor even though wall time and the live
	// checkpoint have changed, and traverses duplicate parents into their children.
	s.Store.Now = func() time.Time { return time.UnixMilli(run.EndMS).Add(time.Hour) }
	interrupted.Version = doc.Version
	if err := (&Service{Store: s.Store}).Recompute(ctx, interrupted); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"parent.mean": "25", "child.mean": "50"} {
		point, err := s.Store.Latest(ctx, "device", key)
		if err != nil || fmt.Sprint(point.Value) != want {
			t.Fatal(key, point, err)
		}
	}
	doc, err = s.Store.Get(ctx, "job", job.ID)
	completed, _ := store.Decode[model.Job](doc)
	if err != nil || completed.Status != "completed" || completed.CursorMS != run.EndMS || completed.ReplayID != job.ReplayID {
		t.Fatal(completed, err)
	}
	var evaluations, inbox int
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM documents WHERE kind='recompute_outputs'").Scan(&evaluations); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.DB.QueryRow("SELECT COUNT(*) FROM inbox WHERE id LIKE $1", job.ReplayID+":evaluation:%").Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	if evaluations != 8 || inbox != 8 {
		t.Fatalf("duplicate evaluation results: outputs=%d inbox=%d", evaluations, inbox)
	}
	t.Logf("parent-child interruption recovered; 4 raw inputs including 2 at one timestamp; 3 windows; first committed cursor=%d; 8 exact output receipts; stable replay=%s; parent=25 child=50", firstCursor, job.ReplayID)
}

func TestReplayCancellationAndNewGenerationProtectCommittedResults(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint("new-generation=", replace), func(t *testing.T) {
			s, job, points := replayFixture(t)
			ctx := context.Background()
			job, run, _, err := s.prepareReplay(ctx, job)
			if err != nil {
				t.Fatal(err)
			}
			replayCtx := context.WithValue(ctx, replayInitialKey{}, job.ReplayID)
			replayCtx = context.WithValue(replayCtx, replayFilterKey{}, run.Selected)
			replayCtx = context.WithValue(replayCtx, replayGuardKey{}, replayGuard{JobID: job.ID, RunID: job.ReplayID})
			if err = s.Process(replayCtx, points[0], true, job.ReplayID); err != nil {
				t.Fatal(err)
			}
			before, err := s.Store.Latest(ctx, "device", "child.mean")
			if err != nil {
				t.Fatal(err)
			}
			if replace {
				job.Status = "pending"
				if _, err = s.Store.Put(ctx, "job", job.ID, job.Version, job); err != nil {
					t.Fatal(err)
				}
			} else {
				task, err := s.Store.Task(ctx, job.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Store.ChangeTask(ctx, task.ID, task.Version, "cancel", model.Actor{}, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.Process(replayCtx, points[1], true, job.ReplayID); err != errReplayInterrupted {
				t.Fatal("stale worker accepted", err)
			}
			after, err := s.Store.Latest(ctx, "device", "child.mean")
			if err != nil || store.Hash(before) != store.Hash(after) {
				t.Fatal("committed result changed", before, after, err)
			}
			latest, _ := s.Store.Get(ctx, "job", job.ID)
			business, _ := store.Decode[model.Job](latest)
			if replace && (business.TaskID == job.TaskID || business.ReplayID != "" || business.CursorMS != 0) {
				t.Fatal(business)
			}
			if !replace && business.Status != "cancelled" {
				t.Fatal(business)
			}
		})
	}
}

func interruptedRollupsFixture(t *testing.T) (*Service, model.Job) {
	t.Helper()
	s, job, points := replayFixture(t)
	ctx := context.Background()
	last := points[len(points)-1]
	last.ID, last.MessageID, last.Value = "next-day", "next-day", 16
	last.ObservedMS, last.ReceivedMS = points[0].ObservedMS+25*3600000, points[0].ObservedMS+25*3600000
	s.Store.Now = func() time.Time { return time.UnixMilli(last.ObservedMS).Add(time.Hour) }
	if err := s.Store.Write(ctx, func(tx *store.Tx) error { return tx.InsertPoint(last) }); err != nil {
		t.Fatal(err)
	}
	job.ToMS = last.ObservedMS
	doc, err := s.Store.Put(ctx, "job", job.ID, job.Version, job)
	if err != nil {
		t.Fatal(err)
	}
	job, _ = store.Decode[model.Job](doc)
	job.Version = doc.Version
	day := time.UnixMilli(points[0].ObservedMS).UTC().Truncate(24 * time.Hour).Add(24 * time.Hour).UnixMilli()
	if _, err := s.Store.DB.Exec(fmt.Sprintf(`CREATE TRIGGER stop_rollup BEFORE INSERT ON rollups WHEN NEW.bucket_ms>=%d BEGIN SELECT RAISE(ABORT,'injected next-day rollup interruption'); END`, day)); err != nil {
		t.Fatal(err)
	}
	if err := s.Recompute(ctx, job); err == nil {
		t.Fatal("rollup interruption was not injected")
	}
	doc, err = s.Store.Get(ctx, "job", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	interrupted, _ := store.Decode[model.Job](doc)
	if interrupted.Status != "completed" || interrupted.ReplayPhase != "rollups" || interrupted.Progress != 1 {
		t.Fatal(interrupted)
	}
	task, err := s.Store.Task(ctx, interrupted.TaskID)
	if err != nil || task.BusinessState != "finalizing" || task.Phase != "rollups" {
		t.Fatal(task, err)
	}
	if _, err := s.Store.DB.Exec("DROP TRIGGER stop_rollup"); err != nil {
		t.Fatal(err)
	}
	interrupted.Version = doc.Version
	return s, interrupted
}

func replayCommittedSnapshot(t *testing.T, s *Service) string {
	t.Helper()
	values := []any{}
	for _, kind := range []string{"engine_state", "alarm", "revision", "recompute_outputs"} {
		docs, err := s.Store.List(context.Background(), kind)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, docs)
	}
	return store.Hash(values)
}

func replayRollupSnapshot(t *testing.T, s *Service) (string, int) {
	t.Helper()
	rows, err := s.Store.DB.Query("SELECT device_id,key,granularity,bucket_ms,data FROM rollups ORDER BY device_id,key,granularity,bucket_ms")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := []any{}
	for rows.Next() {
		var device, key, granularity, raw string
		var bucket int64
		if err := rows.Scan(&device, &key, &granularity, &bucket, &raw); err != nil {
			t.Fatal(err)
		}
		values = append(values, []any{device, key, granularity, bucket, raw})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return store.Hash(values), len(values)
}

func TestReplayResumesRollupsAfterCommittedFinalState(t *testing.T) {
	s, interrupted := interruptedRollupsFixture(t)
	ctx := context.Background()
	committed := replayCommittedSnapshot(t, s)
	partial, count := replayRollupSnapshot(t, s)
	if count == 0 {
		t.Fatal("no cross-day partial rollups were committed")
	}
	task, err := s.Store.Task(ctx, interrupted.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.Store.ChangeTask(ctx, task.ID, task.Version, "cancel", model.Actor{}, nil)
	if err != nil || cancelled.BusinessState != "cancelled" || cancelled.Phase != "rollups" {
		t.Fatal(cancelled, err)
	}
	if err := (&Service{Store: s.Store}).Recompute(ctx, interrupted); err != nil {
		t.Fatal(err)
	}
	if stopped, _ := replayRollupSnapshot(t, s); stopped != partial || replayCommittedSnapshot(t, s) != committed {
		t.Fatal("cancellation changed committed rollups or evaluation state")
	}
	retried, err := s.Store.ChangeTask(ctx, task.ID, cancelled.Version, "retry", model.Actor{}, nil)
	if err != nil || retried.ID != task.ID || retried.RiverID != task.RiverID || retried.Phase != "rollups" {
		t.Fatal(retried, err)
	}
	doc, err := s.Store.Get(ctx, "job", interrupted.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, _ := store.Decode[model.Job](doc)
	resumed.Version = doc.Version
	if err := (&Service{Store: s.Store}).Recompute(ctx, resumed); err != nil {
		t.Fatal(err)
	}
	doc, err = s.Store.Get(ctx, "job", interrupted.ID)
	finished, _ := store.Decode[model.Job](doc)
	if err != nil {
		t.Fatal(err)
	}
	_, rollups := replayRollupSnapshot(t, s)
	if finished.ReplayPhase != "completed" || finished.Status != "completed" || finished.TaskID != interrupted.TaskID || finished.ReplayID != interrupted.ReplayID || replayCommittedSnapshot(t, s) != committed || rollups <= count {
		t.Fatal(finished, count, rollups)
	}
	t.Logf("cross-day failure left %d committed rollups; cancellation preserved state versions and exact output receipts; retry retained task/river/replay identity and completed %d rollups without evaluating inputs again", count, rollups)
}

func TestReplayRollupNewGenerationStopsOldWrites(t *testing.T) {
	s, interrupted := interruptedRollupsFixture(t)
	ctx := context.Background()
	committed := replayCommittedSnapshot(t, s)
	partial, _ := replayRollupSnapshot(t, s)
	next := interrupted
	next.Status = "pending"
	doc, err := s.Store.Put(ctx, "job", next.ID, next.Version, next)
	if err != nil {
		t.Fatal(err)
	}
	next, _ = store.Decode[model.Job](doc)
	if next.TaskID == interrupted.TaskID || next.ReplayID != "" || next.ReplayPhase != "" {
		t.Fatal(next)
	}
	if err := s.Recompute(ctx, interrupted); err != nil {
		t.Fatal(err)
	}
	if stopped, _ := replayRollupSnapshot(t, s); stopped != partial || replayCommittedSnapshot(t, s) != committed {
		t.Fatal("superseded rollup worker changed committed results")
	}
	current, err := s.Store.Get(ctx, "job", next.ID)
	if err != nil || current.Version != doc.Version || string(current.Data) != string(doc.Data) {
		t.Fatal("superseded worker changed new generation", current, err)
	}
	task, err := s.Store.Task(ctx, interrupted.TaskID)
	if err != nil || !task.Superseded {
		t.Fatal(task, err)
	}
}

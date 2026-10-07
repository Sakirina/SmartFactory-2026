package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/pkg/model"
)

func (s *Store) AnalysisSnapshot(ctx context.Context, id string) (historymodel.Snapshot, error) {
	var result historymodel.Snapshot
	var raw, sum string
	err := s.DB.QueryRowContext(ctx, "SELECT sha256,data FROM sf_analysis_snapshots WHERE id=$1", id).Scan(&sum, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if err = DecodeJSON([]byte(raw), &result); err != nil {
		return result, err
	}
	actual, err := historymodel.SnapshotDigest(result)
	if err != nil {
		return result, err
	}
	if actual != sum || result.SHA256 != sum || result.ID != id {
		return result, errors.New("analysis snapshot integrity check failed")
	}
	return result, nil
}

func (t *Tx) saveAnalysisSnapshot(snapshot historymodel.Snapshot) error {
	sum, err := historymodel.SnapshotDigest(snapshot)
	if err != nil {
		return err
	}
	if sum != snapshot.SHA256 || snapshot.ID != "snapshot:"+sum {
		return errors.New("analysis snapshot identity mismatch")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(raw) > historymodel.MaxSnapshotBytes {
		return errors.New("analysis snapshot exceeds 32 MiB budget; reduce the interval")
	}
	_, err = t.ExecContext(t.Ctx, "INSERT INTO sf_analysis_snapshots(id,sha256,data,created_ms) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING", snapshot.ID, sum, string(raw), snapshot.CapturedMS)
	return err
}

func (t *Tx) enqueueAnalysis(run historymodel.Run) error {
	if err := t.lock("task", run.TaskID, false); err != nil {
		return err
	}
	result, err := t.Store.TaskClient.InsertTx(t.Ctx, t.Tx, historymodel.TaskArgs{ID: run.TaskID, RunID: run.ID}, taskOptions(t.Ctx, "history_analysis", time.Time{}))
	if err != nil {
		return err
	}
	resources, err := json.Marshal(normalizeTaskResources(run.Resources))
	if err != nil {
		return err
	}
	_, err = t.ExecContext(t.Ctx, "INSERT INTO sf_tasks(id,river_id,kind,business_id,business_version,outbox_id,resources,created_ms) VALUES($1,$2,'analysis',$3,1,'',$4,$5)", run.TaskID, result.Job.ID, run.ID, string(resources), run.CreatedMS)
	return err
}

func (s *Store) CreateAnalysis(ctx context.Context, run historymodel.Run, snapshot historymodel.Snapshot, actor model.Actor, authorize func(*Tx) error) (historymodel.Run, error) {
	err := s.Write(ctx, func(t *Tx) error {
		if authorize != nil {
			if err := authorize(t); err != nil {
				return err
			}
		}
		if err := t.saveAnalysisSnapshot(snapshot); err != nil {
			return err
		}
		if _, err := t.Put("analysis_run", run.ID, 0, run); err != nil {
			return err
		}
		if err := t.enqueueAnalysis(run); err != nil {
			return err
		}
		return t.Audit(actor, "analysis.create", run.DefinitionID, run.ID, map[string]any{"snapshot_id": snapshot.ID, "snapshot_sha256": snapshot.SHA256, "kind": run.Kind})
	})
	return run, err
}

func (s *Store) AnalysisRun(ctx context.Context, id string) (historymodel.Run, error) {
	doc, err := s.Get(ctx, "analysis_run", id)
	if err != nil {
		return historymodel.Run{}, err
	}
	run, err := Decode[historymodel.Run](doc)
	run.Version = doc.Version
	return run, err
}

func (s *Store) BeginAnalysis(ctx context.Context, id string) (historymodel.Run, error) {
	var result historymodel.Run
	waiting := false
	err := s.Write(ctx, func(t *Tx) error {
		doc, err := t.Get("analysis_run", id)
		if err != nil {
			return err
		}
		run, err := Decode[historymodel.Run](doc)
		if err != nil {
			return err
		}
		run.Version = doc.Version
		result = run
		if run.Status == "cancelled" {
			return context.Canceled
		}
		if run.Status == "completed" {
			return nil
		}
		if run.ParentRunID != "" && !run.InitialStateResolved {
			parentDoc, err := t.Read("analysis_run", run.ParentRunID)
			if err != nil {
				return err
			}
			parent, err := Decode[historymodel.Run](parentDoc)
			if err != nil {
				return err
			}
			if parent.Status != "completed" {
				if run.Status == "waiting_parent" {
					waiting = true
					return nil
				}
				run.Status = "waiting_parent"
				run.Error = "waiting for previous shadow generation " + parent.ID
				waiting = true
			} else {
				var raw string
				if err = t.QueryRowContext(ctx, "SELECT data FROM sf_analysis_snapshots WHERE id=$1", run.SnapshotID).Scan(&raw); err != nil {
					return err
				}
				var snapshot historymodel.Snapshot
				if err = DecodeJSON([]byte(raw), &snapshot); err != nil {
					return err
				}
				sum, err := historymodel.SnapshotDigest(snapshot)
				if err != nil || sum != run.SnapshotSHA256 {
					return errors.New("pending shadow snapshot integrity mismatch")
				}
				snapshot.InitialState = parent.FinalState["candidate"]
				if snapshot.InitialState == nil {
					snapshot.InitialState = historymodel.State{}
				}
				snapshot.InitialStateSource = "previous_shadow_run"
				snapshot.InputSHA256 = historymodel.InputDigest(snapshot)
				sum, err = historymodel.SnapshotDigest(snapshot)
				if err != nil {
					return err
				}
				snapshot.ID = "snapshot:" + sum
				snapshot.SHA256 = sum
				if err = t.saveAnalysisSnapshot(snapshot); err != nil {
					return err
				}
				run.SnapshotID = snapshot.ID
				run.SnapshotSHA256 = sum
				run.InputSHA256 = snapshot.InputSHA256
				run.InitialStateResolved = true
			}
		}
		if !waiting {
			run.Status = "running"
			run.Error = ""
		}
		run.UpdatedMS = s.Now().UnixMilli()
		run.Version++
		if _, err = t.Put("analysis_run", id, doc.Version, run); err != nil {
			return err
		}
		result = run
		return nil
	})
	if err == nil && waiting {
		err = historymodel.ErrParentPending
	}
	return result, err
}

func (s *Store) CommitAnalysisStep(ctx context.Context, run historymodel.Run, step historymodel.Step, states map[string]historymodel.State) (historymodel.Run, error) {
	var result historymodel.Run
	err := s.Write(ctx, func(t *Tx) error {
		doc, err := t.Get("analysis_run", run.ID)
		if err != nil {
			return err
		}
		current, err := Decode[historymodel.Run](doc)
		if err != nil {
			return err
		}
		if current.Status == "cancelled" {
			return context.Canceled
		}
		if doc.Version != run.Version || current.Cursor != step.InputIndex || current.Status != "running" {
			return ErrConflict
		}
		raw, err := json.Marshal(step)
		if err != nil {
			return err
		}
		_, err = t.ExecContext(ctx, "INSERT INTO sf_analysis_steps(run_id,input_index,definition_id,input_identity,input_revision,data) VALUES($1,$2,$3,$4,$5,$6)", run.ID, step.InputIndex, run.DefinitionID, analysisInputIdentity(step.Point), step.Point.Revision, string(raw))
		if err != nil {
			return err
		}
		current.Cursor++
		current.Progress = float64(current.Cursor) / float64(current.Total)
		current.FinalState = states
		current.Version = doc.Version + 1
		current.UpdatedMS = s.Now().UnixMilli()
		if current.Cursor == current.Total {
			current.Status = "completed"
		}
		if _, err = t.Put("analysis_run", run.ID, doc.Version, current); err != nil {
			return err
		}
		if current.Status == "completed" && current.CandidateID != "" {
			cd, err := t.Get("shadow_candidate", current.CandidateID)
			if err != nil {
				return err
			}
			candidate, err := Decode[historymodel.Candidate](cd)
			if err != nil {
				return err
			}
			if candidate.Epoch == current.CandidateVersion && current.InputGeneration > candidate.CompletedGeneration {
				candidate.CompletedGeneration = current.InputGeneration
				candidate.LastRunID = current.ID
				candidate.Version = cd.Version + 1
				if _, err = t.Put("shadow_candidate", candidate.ID, cd.Version, candidate); err != nil {
					return err
				}
			}
		}
		result = current
		return nil
	})
	return result, err
}

func (s *Store) FailAnalysis(ctx context.Context, id string, cause error) error {
	return s.Write(ctx, func(t *Tx) error {
		doc, err := t.Get("analysis_run", id)
		if err != nil {
			return err
		}
		run, err := Decode[historymodel.Run](doc)
		if err != nil {
			return err
		}
		if run.Status == "completed" || run.Status == "cancelled" {
			return nil
		}
		run.Status = "failed"
		run.Error = cause.Error()
		run.Version = doc.Version + 1
		run.UpdatedMS = s.Now().UnixMilli()
		_, err = t.Put("analysis_run", id, doc.Version, run)
		return err
	})
}

func (s *Store) AnalysisSteps(ctx context.Context, id string, after, limit int) (historymodel.StepList, error) {
	result := historymodel.StepList{Items: []historymodel.Step{}, Next: -1}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	if after < 0 {
		after = 0
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT data FROM sf_analysis_steps WHERE run_id=$1 AND input_index>=$2 ORDER BY input_index LIMIT $3", id, after, limit)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return result, err
		}
		var step historymodel.Step
		if err = DecodeJSON([]byte(raw), &step); err != nil {
			rows.Close()
			return result, err
		}
		result.Items = append(result.Items, step)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for i := range result.Items {
		step := &result.Items[i]
		if step.FormalInputID == "" {
			continue
		}
		var raw string
		definition := ""
		if len(step.Lanes) > 0 {
			definition = step.Lanes[0].Evaluation.DefinitionID
		}
		err = s.DB.QueryRowContext(ctx, "SELECT data FROM sf_analysis_formal WHERE definition_id=$1 AND input_identity=$2 AND input_revision=$3 ORDER BY rule_version DESC LIMIT 1", definition, step.FormalInputID, step.Point.Revision).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return result, err
		}
		var evaluation rulecore.Evaluation
		if err = DecodeJSON([]byte(raw), &evaluation); err != nil {
			return result, err
		}
		versions, err := s.Versions(ctx, "definition", evaluation.DefinitionID)
		if err != nil {
			return result, err
		}
		keys := []string{}
		for _, version := range versions {
			definition, err := Decode[model.Definition](version)
			if err != nil {
				return result, err
			}
			if definition.Version == evaluation.Version {
				for _, output := range definition.Outputs {
					keys = append(keys, definition.ID+"."+output.Key)
				}
				break
			}
		}
		if len(keys) > 0 {
			current, err := s.Query(ctx, Query{DeviceIDs: []string{evaluation.EntityID}, Keys: keys, FromMS: evaluation.AtMS, ToMS: evaluation.AtMS, Limit: len(keys)})
			if err != nil {
				return result, err
			}
			step.FormalOutputs = current.Points
		}
		step.FormalEvaluation = &evaluation
		step.FormalStatus = "realtime_evaluation"
		if evaluation.Historical {
			step.FormalStatus = "historical_recompute_projection"
		}
		if len(step.Lanes) > 0 {
			step.FormalDifference = historymodel.Compare(historymodel.Lane{Evaluation: evaluation}, step.Lanes[0])
		}
	}
	if len(result.Items) == limit {
		result.Next = result.Items[len(result.Items)-1].InputIndex + 1
	}
	return result, nil
}

func analysisInputIdentity(p model.Observation) string { return historymodel.InputIdentity(p) }

// Formal explanation receipts share the existing evaluation transaction. They
// describe committed behavior and never schedule additional business effects.
func (t *Tx) RecordAnalysisFormal(d model.Definition, p model.Observation, evaluation rulecore.Evaluation) error {
	raw, err := json.Marshal(evaluation)
	if err != nil {
		return err
	}
	if err = t.SetEphemeral("analysis_formal_receipt", Hash([]any{d.ID, d.Version, analysisInputIdentity(p), p.Revision, evaluation.RunID}), evaluation); err != nil {
		return err
	}
	_, err = t.ExecContext(t.Ctx, "INSERT INTO sf_analysis_formal(definition_id,input_identity,input_revision,rule_version,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT(definition_id,input_identity,input_revision,rule_version) DO UPDATE SET data=excluded.data", d.ID, analysisInputIdentity(p), p.Revision, d.Version, string(raw))
	return err
}

func (s *Store) SaveShadow(ctx context.Context, candidate historymodel.Candidate, expected int64, actor model.Actor, authorize func(*Tx) error) (historymodel.Candidate, error) {
	err := s.Write(ctx, func(t *Tx) error {
		if authorize != nil {
			if err := authorize(t); err != nil {
				return err
			}
		}
		if err := t.saveAnalysisSnapshot(candidate.BaseSnapshot); err != nil {
			return err
		}
		if _, err := t.Put("shadow_candidate", candidate.ID, expected, candidate); err != nil {
			return err
		}
		return t.Audit(actor, "shadow.enable", candidate.DefinitionID, candidate.ID, map[string]any{"candidate_version": candidate.Version, "epoch": candidate.Epoch, "definition_version": candidate.DefinitionVersion, "snapshot": candidate.BaseSnapshot.ID})
	})
	return candidate, err
}

func (s *Store) StopShadow(ctx context.Context, id string, expected int64, actor model.Actor, authorize func(*Tx) error) (historymodel.Candidate, error) {
	var result historymodel.Candidate
	err := s.Write(ctx, func(t *Tx) error {
		if authorize != nil {
			if err := authorize(t); err != nil {
				return err
			}
		}
		doc, err := t.Get("shadow_candidate", id)
		if err != nil {
			return err
		}
		c, err := Decode[historymodel.Candidate](doc)
		if err != nil {
			return err
		}
		if doc.Version != expected || c.Status != "active" {
			return ErrConflict
		}
		c.Status = "stopped"
		c.StoppedMS = s.Now().UnixMilli()
		c.Version = doc.Version + 1
		if _, err = t.Put("shadow_candidate", id, doc.Version, c); err != nil {
			return err
		}
		result = c
		return t.Audit(actor, "shadow.stop", c.DefinitionID, id, map[string]any{"epoch": c.Epoch, "generation": c.Generation})
	})
	return result, err
}

func (s *Store) ShadowInputs(ctx context.Context, id string, epoch int64) ([]model.Observation, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT input_identity,input_revision,data FROM sf_shadow_inputs WHERE candidate_id=$1 AND epoch=$2 ORDER BY generation", id, epoch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indices := map[string]int{}
	points := []model.Observation{}
	for rows.Next() {
		var identity, raw string
		var revision int64
		if err = rows.Scan(&identity, &revision, &raw); err != nil {
			return nil, err
		}
		var p model.Observation
		if err = DecodeJSON([]byte(raw), &p); err != nil {
			return nil, err
		}
		if i, ok := indices[identity]; ok {
			if points[i].Revision <= revision {
				points[i] = p
			}
		} else {
			indices[identity] = len(points)
			points = append(points, p)
		}
	}
	return points, rows.Err()
}

func (s *Store) ShadowInputKnown(ctx context.Context, id string, epoch int64, p model.Observation) (bool, error) {
	var sum string
	err := s.DB.QueryRowContext(ctx, "SELECT payload_hash FROM sf_shadow_inputs WHERE candidate_id=$1 AND epoch=$2 AND input_identity=$3 AND input_revision=$4", id, epoch, analysisInputIdentity(p), p.Revision).Scan(&sum)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if sum != Hash(p) {
		return false, fmt.Errorf("shadow input content differs for the same identity and revision: %w", ErrConflict)
	}
	return true, nil
}

func (s *Store) CaptureShadow(ctx context.Context, candidate historymodel.Candidate, p model.Observation, run historymodel.Run, snapshot historymodel.Snapshot) error {
	return s.Write(ctx, func(t *Tx) error {
		doc, err := t.Get("shadow_candidate", candidate.ID)
		if err != nil {
			return err
		}
		current, err := Decode[historymodel.Candidate](doc)
		if err != nil {
			return err
		}
		if current.Status != "active" || current.Epoch != candidate.Epoch {
			return nil
		}
		var old string
		err = t.QueryRowContext(ctx, "SELECT payload_hash FROM sf_shadow_inputs WHERE candidate_id=$1 AND epoch=$2 AND input_identity=$3 AND input_revision=$4", candidate.ID, candidate.Epoch, analysisInputIdentity(p), p.Revision).Scan(&old)
		if err == nil {
			if old != Hash(p) {
				return fmt.Errorf("shadow input identity content mismatch: %w", ErrConflict)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if doc.Version != candidate.Version {
			return ErrConflict
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = t.ExecContext(ctx, "INSERT INTO sf_shadow_inputs(candidate_id,epoch,input_identity,input_revision,generation,payload_hash,data) VALUES($1,$2,$3,$4,$5,$6,$7)", candidate.ID, candidate.Epoch, analysisInputIdentity(p), p.Revision, candidate.Generation+1, Hash(p), string(raw))
		if err != nil {
			return err
		}
		if err = t.saveAnalysisSnapshot(snapshot); err != nil {
			return err
		}
		if _, err = t.Put("analysis_run", run.ID, 0, run); err != nil {
			return err
		}
		if err = t.enqueueAnalysis(run); err != nil {
			return err
		}
		current.Generation++
		current.Version = doc.Version + 1
		current.Context = candidate.Context
		current.LastInput = candidate.LastInput
		current.InputCount = candidate.InputCount
		current.Resources = normalizeTaskResources(append(current.Resources, snapshot.Resources...))
		_, err = t.Put("shadow_candidate", candidate.ID, doc.Version, current)
		return err
	})
}

func (s *Store) PauseShadow(ctx context.Context, id string, expected int64, reason string) error {
	return s.Write(ctx, func(t *Tx) error {
		doc, err := t.Get("shadow_candidate", id)
		if err != nil {
			return err
		}
		if doc.Version != expected {
			return ErrConflict
		}
		c, err := Decode[historymodel.Candidate](doc)
		if err != nil {
			return err
		}
		if c.Status != "active" {
			return nil
		}
		c.Status = "budget_exceeded"
		c.Error = reason
		c.Version = doc.Version + 1
		if _, err = t.Put("shadow_candidate", id, doc.Version, c); err != nil {
			return err
		}
		return t.Audit(model.Actor{UserID: "system"}, "shadow.budget", c.DefinitionID, id, map[string]any{"reason": reason})
	})
}

func (s *Store) fillAnalysisTask(ctx context.Context, tx *sql.Tx, task model.Task) (model.Task, error) {
	doc, err := scanDocument(tx.QueryRowContext(ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind='analysis_run' AND id=$1", task.BusinessID))
	if err != nil {
		return task, err
	}
	run, err := Decode[historymodel.Run](doc)
	if err != nil {
		return task, err
	}
	task.BusinessState = run.Status
	task.Progress = run.Progress
	task.Phase = "analysis"
	if run.Status == "completed" {
		task.Phase = "completed"
	}
	task.ReplayID = run.ID
	if run.Error != "" {
		task.Error = run.Error
	}
	// The input snapshot, task/run generation and business stage define the
	// operation. Cursor, accumulated state and timestamps are progress within
	// that same operation; advancing them must leave time to confirm a cancel.
	run.Version, run.UpdatedMS, run.Cursor, run.Progress = 0, 0, 0, 0
	run.FinalState = nil
	if !run.InitialStateResolved && run.ParentRunID != "" && (run.Status == "pending" || run.Status == "waiting_parent") {
		run.Status = "waiting_parent"
		run.Error = ""
	}
	run.Status = taskBusinessOperationState(run.Status)
	task.Version = Hash([]any{task.Version, run})
	return task, nil
}

func (t *Tx) changeAnalysisTask(id, action string) error {
	doc, err := t.Get("analysis_run", id)
	if err != nil {
		return err
	}
	run, err := Decode[historymodel.Run](doc)
	if err != nil {
		return err
	}
	if run.Status == "completed" {
		return ErrConflict
	}
	if action == "cancel" {
		run.Status = "cancelled"
	} else {
		run.Status = "pending"
		run.Error = ""
	}
	run.Version = doc.Version + 1
	run.UpdatedMS = t.Store.Now().UnixMilli()
	_, err = t.Put("analysis_run", id, doc.Version, run)
	return err
}

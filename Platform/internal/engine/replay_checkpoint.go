package engine

import (
	"context"
	"errors"
	"fmt"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// A run freezes its range, selected definitions, source devices and initial
// checkpoint snapshot. CursorMS advances only after a complete replay window.
type replayRun struct {
	StartMS  int64           `json:"start_ms"`
	EndMS    int64           `json:"end_ms"`
	Selected map[string]bool `json:"selected"`
	Devices  []string        `json:"devices"`
}
type replayGuard struct{ JobID, RunID string }
type replayGuardKey struct{}
type replayInitialKey struct{}

var errReplayInterrupted = errors.New("replay generation was cancelled or replaced")

func (s *Service) prepareReplay(ctx context.Context, job model.Job) (model.Job, replayRun, int64, error) {
	run := replayRun{}
	initial := job.ReplayID == ""
	var err error
	if initial {
		run.Selected, run.Devices, err = s.affected(ctx, job)
		if err != nil {
			return job, run, 0, err
		}
		run.StartMS, run.EndMS, err = s.rawRange(ctx, run.Devices)
		if err != nil {
			return job, run, 0, err
		}
		cutoff := s.Store.Now().AddDate(0, 0, -s.Store.Policy().Retention.RawDays).UnixMilli()
		if run.StartMS < cutoff {
			run.StartMS = cutoff
		}
		if job.FromMS < cutoff {
			job.Error = "raw history before retention is unavailable"
		}
		if run.EndMS < job.ToMS {
			run.EndMS = job.ToMS
		}
		job.ReplayID = job.TaskID
		if job.ReplayID == "" {
			job.ReplayID = fmt.Sprintf("%s:%d", job.ID, job.Version+1)
		}
		job.ReplayStartMS, job.ReplayEndMS = run.StartMS, run.EndMS
		job.ReplayPhase = "replaying"
		job.CursorMS, job.Progress = 0, 0
	} else {
		doc, e := s.Store.Get(ctx, "recompute_run", job.ReplayID)
		if e != nil {
			return job, run, 0, e
		}
		if err = store.DecodeJSON(doc.Data, &run); err != nil {
			return job, run, 0, err
		}
		if run.StartMS != job.ReplayStartMS || run.EndMS != job.ReplayEndMS {
			return job, run, 0, store.ErrConflict
		}
	}
	expected := job.Version
	job.Status, job.Version = "running", expected+1
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Put("job", job.ID, expected, job); err != nil {
			return err
		}
		if !initial {
			return nil
		}
		if err := tx.SetEphemeral("recompute_run", job.ReplayID, run); err != nil {
			return err
		}
		// One statement captures the predecessor of every state at the same
		// database snapshot; later realtime checkpoint writes cannot change it.
		rows, err := tx.QueryContext(ctx, `SELECT c.state_id,c.data FROM engine_checkpoints c WHERE c.at_ms<$1 AND NOT EXISTS (SELECT 1 FROM engine_checkpoints newer WHERE newer.state_id=c.state_id AND newer.at_ms<$1 AND newer.at_ms>c.at_ms)`, run.StartMS)
		if err != nil {
			return err
		}
		type initialState struct {
			id     string
			states map[string]RuntimeState
		}
		initialStates := []initialState{}
		for rows.Next() {
			var id, raw string
			if err = rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			states := map[string]RuntimeState{}
			if err = store.DecodeJSON([]byte(raw), &states); err != nil {
				rows.Close()
				return err
			}
			initialStates = append(initialStates, initialState{id, states})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, item := range initialStates {
			if err = tx.SetEphemeral("recompute_initial", job.ReplayID+":"+item.id, item.states); err != nil {
				return err
			}
		}
		return nil
	})
	return job, run, job.Version, err
}

// Output identity and every derived field are committed with the inbox marker
// and state update. Recovery replays these outputs into dependent definitions.
type replayEvaluation struct {
	InputHash string              `json:"input_hash"`
	Outputs   []model.Observation `json:"outputs"`
}

func decodeReplayOutputs(document store.Document, input model.Observation) ([]model.Observation, error) {
	stored, err := store.Decode[replayEvaluation](document)
	if err != nil {
		return nil, err
	}
	if stored.InputHash != store.Hash(input) {
		return nil, store.ErrConflict
	}
	return stored.Outputs, nil
}

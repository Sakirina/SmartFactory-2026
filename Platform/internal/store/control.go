package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"competition2026/product/platform/pkg/model"
)

type ControlAction struct {
	CommandID   string           `json:"command_id"`
	PayloadHash string           `json:"payload_hash"`
	Status      string           `json:"status"`
	Fence       uint64           `json:"fence"`
	Result      model.StepResult `json:"result"`
	UpdatedMS   int64            `json:"updated_ms"`
}

func scanControlAction(row interface{ Scan(...any) error }) (ControlAction, error) {
	var a ControlAction
	var raw string
	err := row.Scan(&a.CommandID, &a.PayloadHash, &a.Status, &a.Fence, &raw, &a.UpdatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err == nil {
		err = DecodeJSON([]byte(raw), &a.Result)
	}
	return a, err
}

func (s *Store) ControlAction(ctx context.Context, id string) (ControlAction, error) {
	return scanControlAction(s.DB.QueryRowContext(ctx, "SELECT command_id,payload_hash,status,fence,data,updated_ms FROM action_journal WHERE command_id=$1", id))
}
func (t *Tx) ControlAction(id string) (ControlAction, error) {
	return scanControlAction(t.QueryRowContext(t.Ctx, "SELECT command_id,payload_hash,status,fence,data,updated_ms FROM action_journal WHERE command_id=$1", id))
}

// ReserveControlAction is called while the execution document is locked. The
// command identity persists through cancellation, restart and manual recovery.
func (t *Tx) ReserveControlAction(a ControlAction) (ControlAction, bool, error) {
	old, err := t.ControlAction(a.CommandID)
	if err == nil {
		if old.PayloadHash != a.PayloadHash {
			return old, false, ErrConflict
		}
		return old, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return old, false, err
	}
	a.Status = "reserved"
	a.UpdatedMS = t.Store.Now().UnixMilli()
	raw, err := json.Marshal(a.Result)
	if err != nil {
		return a, false, err
	}
	_, err = t.ExecContext(t.Ctx, "INSERT INTO action_journal(command_id,payload_hash,status,fence,data,updated_ms) VALUES($1,$2,$3,$4,$5,$6)", a.CommandID, a.PayloadHash, a.Status, a.Fence, string(raw), a.UpdatedMS)
	return a, err == nil, err
}

func (t *Tx) FinishControlAction(id, payloadHash string, r model.StepResult) error {
	previous, err := t.ControlAction(id)
	if err != nil {
		return err
	}
	if previous.PayloadHash != payloadHash || r.CommandID != id || r.StepID != previous.Result.StepID {
		return ErrConflict
	}
	// A later unresolved result cannot erase a confirmed outcome; contradictory
	// terminal feedback is retained as evidence by the caller, then rejected.
	if previous.Status != "reserved" && previous.Status != "RESULT_UNKNOWN" {
		if previous.Result.Status != r.Status {
			return ErrConflict
		}
		return nil
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = t.ExecContext(t.Ctx, "UPDATE action_journal SET status=$1,data=$2,updated_ms=$3 WHERE command_id=$4", r.Status, string(raw), t.Store.Now().UnixMilli(), id)
	return err
}

func executionActions(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string) ([]ControlAction, error) {
	prefix := id + ":"
	rows, err := db.QueryContext(ctx, "SELECT command_id,payload_hash,status,fence,data,updated_ms FROM action_journal WHERE substr(command_id,1,$1)=$2 ORDER BY command_id", utf8.RuneCountInString(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ControlAction{}
	for rows.Next() {
		a, err := scanControlAction(rows)
		if err != nil {
			return nil, err
		}
		if a.CommandID == prefix+a.Result.StepID {
			out = append(out, a)
		}
	}
	return out, rows.Err()
}
func (s *Store) ExecutionActions(ctx context.Context, id string) ([]ControlAction, error) {
	return executionActions(ctx, s.DB, id)
}
func (t *Tx) ExecutionActions(id string) ([]ControlAction, error) {
	return executionActions(t.Ctx, t.Tx, id)
}

// PersistExecution atomically writes the version, transition, signed audit and
// optional edge receipt. Domain transition rules are applied by control first.
func (t *Tx) PersistExecution(req model.Execution, expected int64, transition model.ExecutionTransition, receiptNode string) error {
	if req.Version != expected+1 || transition.Version != req.Version || transition.ExecutionID != req.DownlinkID {
		return ErrConflict
	}
	if _, err := t.Put("execution", req.DownlinkID, expected, req); err != nil {
		return err
	}
	raw, err := json.Marshal(transition)
	if err != nil {
		return err
	}
	if _, err = t.ExecContext(t.Ctx, "INSERT INTO execution_transitions(execution_id,version,at_ms,data) VALUES($1,$2,$3,$4)", req.DownlinkID, req.Version, transition.AtMS, string(raw)); err != nil {
		return err
	}
	if receiptNode != "" {
		evidence, err := t.ExecutionEvidence(req.DownlinkID)
		if err != nil {
			return err
		}
		receipt := model.ExecutionReceipt{Execution: req, ReceiptSource: &model.ExecutionReceiptSource{SchemaVersion: 1, NodeID: receiptNode, Version: req.Version, RecordedMS: transition.AtMS}, CommandEvidence: evidence}
		if err = t.Enqueue(fmt.Sprintf("receipt:%s:%d", req.DownlinkID, req.Version), "cloud_receipt", receiptNode, receipt); err != nil {
			return err
		}
	}
	return t.Audit(transition.Actor, "control."+transition.Event, req.DefinitionID, req.DownlinkID, map[string]any{"transition": transition, "execution": req})
}

func (t *Tx) SaveControlEvidence(e model.CommandEvidence) error {
	if err := t.lock("execution-evidence", e.ExecutionID, false); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = t.ExecContext(t.Ctx, "INSERT INTO control_evidence(id,execution_id,command_id,collected_ms,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING", e.ID, e.ExecutionID, e.CommandID, e.CollectedMS, string(raw))
	if err != nil {
		return err
	}
	var saved string
	if err = t.QueryRowContext(t.Ctx, "SELECT data FROM control_evidence WHERE id=$1", e.ID).Scan(&saved); err != nil {
		return err
	}
	var previous model.CommandEvidence
	if err = DecodeJSON([]byte(saved), &previous); err != nil {
		return err
	}
	if Hash(previous) != Hash(e) {
		return ErrConflict
	}
	t.markQueryDocument("execution", e.ExecutionID)
	return nil
}

func (s *Store) ExecutionTransitions(ctx context.Context, id string) ([]model.ExecutionTransition, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT data FROM execution_transitions WHERE execution_id=$1 ORDER BY version", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ExecutionTransition{}
	for rows.Next() {
		var raw string
		var item model.ExecutionTransition
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := DecodeJSON([]byte(raw), &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) ExecutionEvidence(ctx context.Context, id string) ([]model.CommandEvidence, error) {
	return executionEvidence(ctx, s.DB, id)
}
func (t *Tx) ExecutionEvidence(id string) ([]model.CommandEvidence, error) {
	if err := t.lock("execution-evidence", id, false); err != nil {
		return nil, err
	}
	return executionEvidence(t.Ctx, t.Tx, id)
}
func executionEvidence(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string) ([]model.CommandEvidence, error) {
	rows, err := db.QueryContext(ctx, "SELECT data FROM control_evidence WHERE execution_id=$1 ORDER BY collected_ms,id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.CommandEvidence{}
	for rows.Next() {
		var raw string
		var item model.CommandEvidence
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := DecodeJSON([]byte(raw), &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

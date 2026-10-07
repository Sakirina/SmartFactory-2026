package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"go.opentelemetry.io/otel/trace"
)

var ErrTransition = errors.New("execution state does not accept this event")

// State events are the single production mutation path for manual requests,
// workers, recovered checkpoints, remote steps and cloud receipts.
var transitionTable = map[string]map[string][]string{
	"request":             {"": {"awaiting_approval", "rejected"}},
	"accept":              {"": {"queued"}},
	"remote_accept":       {"": {"running", "degraded_running"}},
	"cancel_before_start": {"": {"cancelled"}},
	"approve":             {"awaiting_approval": {"awaiting_approval", "approved"}, "approved": {"approved"}},
	"dispatch":            {"approved": {"queued"}},
	"start":               {"queued": {"running", "degraded_running"}, "running": {"running", "degraded_running"}, "degraded_running": {"degraded_running"}},
	"action_reserved":     {"running": {"running"}, "degraded_running": {"degraded_running"}},
	"step":                {"running": {"running"}, "degraded_running": {"degraded_running"}, "cancelled_result_unknown": {"cancelled_result_unknown", "cancelled"}, "cancelled": {"cancelled"}},
	"finish":              {"running": {"completed", "failed", "result_unknown"}, "degraded_running": {"degraded_completed", "failed", "result_unknown"}},
	"reject":              {"awaiting_approval": {"rejected"}, "approved": {"rejected"}, "queued": {"rejected"}, "running": {"rejected", "result_unknown"}, "degraded_running": {"rejected", "result_unknown"}, "ready_to_resume": {"rejected"}},
	"cancel":              {"awaiting_approval": {"cancelled"}, "approved": {"cancelled"}, "queued": {"cancelled"}, "running": {"cancelled", "cancelled_result_unknown"}, "degraded_running": {"cancelled", "cancelled_result_unknown"}, "result_unknown": {"cancelled_result_unknown"}, "ready_to_resume": {"cancelled"}},
	"reconcile":           {"result_unknown": {"result_unknown", "ready_to_resume", "completed", "degraded_completed", "failed"}, "cancelled_result_unknown": {"cancelled_result_unknown", "cancelled"}},
	"resume":              {"ready_to_resume": {"queued"}},
}

func ValidTransition(from, to, event string) bool {
	return has(transitionTable[event][from], to)
}

func isRunning(status string) bool { return status == "running" || status == "degraded_running" }
func terminal(status string) bool {
	return has([]string{"completed", "degraded_completed", "failed", "rejected", "result_unknown", "ready_to_resume", "cancelled", "cancelled_result_unknown"}, status)
}
func knownState(state string) bool {
	return state == "awaiting_approval" || state == "approved" || state == "queued" || isRunning(state) || terminal(state)
}

// CheckpointProgress validates signed site journals with the same state and
// confirmed-result rules used by execution receipts.
func CheckpointProgress(previous, next model.Execution) bool {
	if !sameRequest(previous, next) || previous.Fence > next.Fence || !receiptTransition(previous, next) {
		return false
	}
	if previous.Fence == next.Fence {
		if next.Version < previous.Version {
			return false
		}
		if next.Version == previous.Version {
			return store.Hash(previous) == store.Hash(next)
		}
		if terminal(previous.Status) && !(previous.Status == "cancelled_result_unknown" && (next.Status == "cancelled_result_unknown" || next.Status == "cancelled")) {
			return false
		}
	}
	return true
}

func sameRequest(a, b model.Execution) bool {
	return a.DownlinkID == b.DownlinkID && a.DefinitionID == b.DefinitionID && a.DefinitionVersion == b.DefinitionVersion && a.Binding == b.Binding && a.Override == b.Override && store.Hash(a.Params) == store.Hash(b.Params)
}

func receiptTransition(previous, next model.Execution) bool {
	if !knownState(next.Status) {
		return false
	}
	if previous.Status == "" {
		return true
	}
	if previous.CancelRequestedMS > 0 && next.CancelRequestedMS == 0 {
		return false
	}
	for _, prior := range previous.Steps {
		if !has([]string{"SUCCESS", "FAILURE", "REJECTED"}, prior.Status) {
			continue
		}
		found := false
		for _, step := range next.Steps {
			found = found || step.CommandID == prior.CommandID && step.Status == prior.Status && (prior.EvidenceID == "" || step.EvidenceID == prior.EvidenceID)
		}
		if !found {
			return false
		}
	}
	if previous.Status == next.Status {
		return true
	}
	switch previous.Status {
	case "completed", "degraded_completed", "failed", "rejected", "cancelled":
		return false
	case "cancelled_result_unknown":
		return next.Status == "cancelled"
	case "result_unknown":
		if next.Status == "ready_to_resume" || next.Status == "completed" || next.Status == "degraded_completed" || next.Status == "failed" || next.Status == "cancelled_result_unknown" || next.Status == "cancelled" {
			return true
		}
		return next.ResumeActor != nil && next.ReconciledMS > 0 && (previous.Fence == 0 || next.Fence > previous.Fence) && (next.Status == "queued" || isRunning(next.Status))
	case "ready_to_resume":
		return next.ResumeActor != nil || next.Status == "cancelled"
	case "running", "degraded_running":
		return terminal(next.Status) || next.Status == "degraded_running"
	case "queued":
		return isRunning(next.Status) || terminal(next.Status)
	case "approved":
		return next.Status == "queued" || isRunning(next.Status) || terminal(next.Status)
	case "awaiting_approval":
		return next.Status == "approved" || next.Status == "rejected" || next.Status == "cancelled"
	}
	return false
}

type TransitionOptions struct {
	Actor         model.Actor
	Reason        string
	Source        string
	SourceVersion int64
	CommandID     string
	EvidenceIDs   []string
	ReceiptNode   string
}

// ApplyTransition runs inside the business transaction, after authorization
// revisions have been checked and before the commit-log finalization lock.
func ApplyTransition(tx *store.Tx, next *model.Execution, expected int64, event string, options TransitionOptions) error {
	if expected < 0 {
		return store.ErrConflict
	}
	var previous model.Execution
	doc, err := tx.Get("execution", next.DownlinkID)
	if err == nil {
		if doc.Version != expected {
			return store.ErrConflict
		}
		previous, err = store.Decode[model.Execution](doc)
		if err != nil {
			return err
		}
		if !sameRequest(previous, *next) {
			return store.ErrConflict
		}
	} else if !errors.Is(err, store.ErrNotFound) || expected != 0 {
		if errors.Is(err, store.ErrNotFound) {
			return store.ErrConflict
		}
		return err
	}
	allowed := ValidTransition(previous.Status, next.Status, event)
	if event == "receipt" || event == "checkpoint_recovered" {
		allowed = receiptTransition(previous, *next)
	}
	if !allowed {
		return fmt.Errorf("%w: %s -> %s (%s)", ErrTransition, previous.Status, next.Status, event)
	}
	if options.Source == "" {
		options.Source = "control"
	}
	if options.Actor.UserID == "" {
		options.Actor = next.Actor
	}
	next.Version = expected + 1
	transition := model.ExecutionTransition{ExecutionID: next.DownlinkID, Version: next.Version, PreviousVersion: expected, Event: event, From: previous.Status, To: next.Status, AtMS: tx.Store.Now().UnixMilli(), Source: options.Source, SourceVersion: options.SourceVersion, Actor: options.Actor, Reason: options.Reason, CommandID: options.CommandID, EvidenceIDs: options.EvidenceIDs}
	if sc := trace.SpanContextFromContext(tx.Ctx); sc.IsValid() {
		transition.TraceID = sc.TraceID().String()
	}
	return tx.PersistExecution(*next, expected, transition, options.ReceiptNode)
}

func (s *Service) transitionOptions(req model.Execution) TransitionOptions {
	result := TransitionOptions{Actor: req.Actor, Reason: req.Reason}
	if s.Edge {
		result.ReceiptNode = s.NodeID
	}
	return result
}

type saveRequest struct {
	Context   context.Context
	Execution *model.Execution
	Event     string
	Options   TransitionOptions
	Revisions []store.Revision
}

func (s *Service) saveTransition(ctxReq saveRequest) error {
	req := *ctxReq.Execution
	err := s.Store.Write(ctxReq.Context, func(tx *store.Tx) error {
		if err := tx.CheckRevisions(ctxReq.Revisions); err != nil {
			return err
		}
		return ApplyTransition(tx, &req, ctxReq.Execution.Version, strings.TrimPrefix(ctxReq.Event, "control."), ctxReq.Options)
	})
	if err == nil {
		*ctxReq.Execution = req
	}
	return err
}

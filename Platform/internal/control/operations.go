package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"go.opentelemetry.io/otel/trace"
)

type operationKey struct{}
type processingOperation struct {
	ID        string
	StartedMS int64
}

func operationFinished(status string) bool {
	return status == "completed" || status == "rejected" || status == "expired"
}
func (s *Service) operation(ctx context.Context, id string) (model.ControlOperation, error) {
	doc, err := s.Store.Get(ctx, "control_operation", id)
	if err != nil {
		return model.ControlOperation{}, err
	}
	op, err := store.Decode[model.ControlOperation](doc)
	op.Version = doc.Version
	return op, err
}

func (s *Service) sourceRevision(ctx context.Context, req model.Execution, d model.Definition) (string, int64, error) {
	if s.Edge {
		return s.NodeID, req.Version, nil
	}
	node := req.CoordinatorID
	if node == "" {
		node = firstEdge(d)
	}
	doc, err := s.Store.Get(ctx, "receipt_cursor", node+":"+req.DownlinkID)
	if errors.Is(err, store.ErrNotFound) {
		return node, 0, nil
	}
	if err != nil {
		return node, 0, err
	}
	version, err := store.Decode[int64](doc)
	return node, version, err
}

// SubmitOperation persists a cloud request separately from execution state.
// Edge operations use the same use cases and complete the request atomically
// with the resulting execution revision.
func (s *Service) SubmitOperation(ctx context.Context, p identity.Principal, id, action string, input model.ExecutionAction) (model.ControlOperation, error) {
	if !has([]string{"cancel", "reconcile", "resume"}, action) {
		return model.ControlOperation{}, ErrTransition
	}
	if err := validateIntervention(input); err != nil {
		return model.ControlOperation{}, err
	}
	if utf8.RuneCountInString(input.Reason) > 2000 || utf8.RuneCountInString(input.OperationID) > 200 {
		return model.ControlOperation{}, errors.New("operation identifier or reason exceeds its limit")
	}
	req, err := s.Get(ctx, id)
	if err != nil {
		return model.ControlOperation{}, err
	}
	p, d, access, err := s.manualAccess(ctx, p, req, action == "resume")
	if err != nil {
		return model.ControlOperation{}, err
	}
	requestHash := store.Hash([]any{id, action, input.ExpectedVersion, input.ExpectedSourceVersion, input.DeadlineMS, input.Reason, p.User.ID})
	if input.OperationID != "" {
		if old, e := s.operation(ctx, input.OperationID); e == nil {
			if old.RequestHash != requestHash || old.ExecutionID != id || old.Actor.UserID != p.User.ID {
				return old, store.ErrConflict
			}
			if s.Edge && !operationFinished(old.Status) {
				if err := s.ProcessOperation(ctx, model.ControlOperationEnvelope{Operation: old, Execution: req}); err != nil {
					if errors.Is(err, ErrLeaseHeld) || errors.Is(err, store.ErrRetryable) {
						return s.operation(ctx, old.ID)
					}
					return old, err
				}
				return s.operation(ctx, old.ID)
			}
			return old, nil
		} else if !errors.Is(e, store.ErrNotFound) {
			return model.ControlOperation{}, e
		}
	}
	if req.Version != input.ExpectedVersion {
		return model.ControlOperation{}, store.ErrConflict
	}
	if _, ok := transitionTable[action][req.Status]; !ok {
		return model.ControlOperation{}, ErrTransition
	}
	node, sourceVersion, err := s.sourceRevision(ctx, req, d)
	if err != nil {
		return model.ControlOperation{}, err
	}
	if node == "" {
		return model.ControlOperation{}, errors.New("execution has no target edge")
	}
	if !s.Edge && input.ExpectedSourceVersion == nil {
		return model.ControlOperation{}, errors.New("expected_source_version is required for cloud intervention")
	}
	if input.ExpectedSourceVersion != nil && *input.ExpectedSourceVersion != sourceVersion {
		return model.ControlOperation{}, store.ErrConflict
	}
	if input.OperationID == "" {
		input.OperationID = identity.ID()
	}
	now := s.Store.CurrentTime().UnixMilli()
	deadline := input.DeadlineMS
	if deadline == 0 {
		deadline = now + s.Store.Policy().ApprovalTTLMS
	}
	if deadline <= now || deadline > now+int64((24*time.Hour)/time.Millisecond) {
		return model.ControlOperation{}, errors.New("operation deadline must be within the next 24 hours")
	}
	op := model.ControlOperation{ID: input.OperationID, ExecutionID: id, Action: action, Status: "pending", OriginNodeID: s.NodeID, TargetNodeID: node, ExpectedVersion: input.ExpectedVersion, ExpectedSourceVersion: sourceVersion, RequestedMS: now, DeadlineMS: deadline, Reason: input.Reason, Actor: p.Actor, RequestHash: requestHash, Version: 1}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		op.TraceID = sc.TraceID().String()
	}
	envelope := model.ControlOperationEnvelope{Operation: op, Execution: req}
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.CheckRevisions(access.revisions); err != nil {
			return err
		}
		current, err := tx.Get("execution", id)
		if err != nil {
			return err
		}
		if current.Version != input.ExpectedVersion {
			return store.ErrConflict
		}
		if old, err := tx.Get("control_operation", op.ID); err == nil {
			prior, err := store.Decode[model.ControlOperation](old)
			if err != nil {
				return err
			}
			if prior.RequestHash != requestHash {
				return store.ErrConflict
			}
			op = prior
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if _, err = tx.Put("control_operation", op.ID, 0, op); err != nil {
			return err
		}
		if err = tx.Enqueue("control-op:"+op.ID, "edge_control_operation", node, envelope); err != nil {
			return err
		}
		return tx.Audit(p.Actor, "control.operation.request", req.DefinitionID, id, op)
	})
	if err != nil {
		return op, err
	}
	if s.Edge {
		if err = s.fault(ctx, "after_operation_persist", req, op.ID); err != nil {
			return op, err
		}
		if err = s.ProcessOperation(ctx, envelope); err != nil {
			if errors.Is(err, ErrLeaseHeld) || errors.Is(err, store.ErrRetryable) {
				return s.operation(ctx, op.ID)
			}
			return op, err
		}
		return s.operation(ctx, op.ID)
	}
	return op, nil
}

func (s *Service) Operation(ctx context.Context, p identity.Principal, id string) (model.ControlOperation, error) {
	op, err := s.operation(ctx, id)
	if err != nil {
		return op, err
	}
	req, err := s.Get(ctx, op.ExecutionID)
	if err != nil {
		return op, err
	}
	_, err = s.executionReadAccess(ctx, p, req)
	if err == nil && op.Result != nil {
		_, err = s.executionReadAccess(ctx, p, *op.Result)
	}
	return op, err
}

var errOperationExpired = errors.New("operation expired before edge processing")

// claimOperation records the first processing time before any remote lookup.
// A redelivery keeps that time, including after process or transport recovery.
func (s *Service) claimOperation(ctx context.Context, id string) (model.ControlOperation, error) {
	var op model.ControlOperation
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		doc, err := tx.Get("control_operation", id)
		if err != nil {
			return err
		}
		op, err = store.Decode[model.ControlOperation](doc)
		if err != nil {
			return err
		}
		op.Version = doc.Version
		if operationFinished(op.Status) || op.StartedMS != 0 {
			return nil
		}
		now := s.Store.CurrentTime().UnixMilli()
		if now >= op.DeadlineMS {
			return errOperationExpired
		}
		if now < op.RequestedMS-5000 {
			return errors.New("operation clock precedes request time")
		}
		op.StartedMS = now
		op.Version = doc.Version + 1
		if _, err = tx.Put("control_operation", op.ID, doc.Version, op); err != nil {
			return err
		}
		if err = tx.Audit(op.Actor, "control.operation.started", op.ExecutionID, op.ExecutionID, op); err != nil {
			return err
		}
		if op.OriginNodeID != s.NodeID {
			return tx.Enqueue(fmt.Sprintf("control-operation-receipt:%s:%d", op.ID, op.Version), "cloud_operation_receipt", s.NodeID, op)
		}
		return nil
	})
	return op, err
}

func (s *Service) finishOperationTx(ctx context.Context, tx *store.Tx, req model.Execution) error {
	processing, _ := ctx.Value(operationKey{}).(processingOperation)
	id := processing.ID
	if id == "" {
		return nil
	}
	doc, err := tx.Get("control_operation", id)
	if err != nil {
		return err
	}
	op, err := store.Decode[model.ControlOperation](doc)
	if err != nil {
		return err
	}
	if operationFinished(op.Status) {
		return store.ErrConflict
	}
	if processing.StartedMS != op.StartedMS || op.StartedMS < op.RequestedMS-5000 || op.StartedMS >= op.DeadlineMS {
		return errOperationExpired
	}
	op.Status = "completed"
	op.StartedMS = processing.StartedMS
	op.ProcessedMS = s.Store.CurrentTime().UnixMilli()
	op.ResultVersion = req.Version
	op.Result = &req
	op.Version = doc.Version + 1
	if _, err = tx.Put("control_operation", id, doc.Version, op); err != nil {
		return err
	}
	if err = tx.Audit(op.Actor, "control.operation.completed", req.DefinitionID, req.DownlinkID, op); err != nil {
		return err
	}
	if op.OriginNodeID != s.NodeID {
		return tx.Enqueue(fmt.Sprintf("control-operation-receipt:%s:%d", id, op.Version), "cloud_operation_receipt", s.NodeID, op)
	}
	return nil
}

func (s *Service) rejectOperation(ctx context.Context, op model.ControlOperation, status string, cause error) error {
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		doc, err := tx.Get("control_operation", op.ID)
		if err != nil {
			return err
		}
		current, err := store.Decode[model.ControlOperation](doc)
		if err != nil {
			return err
		}
		if operationFinished(current.Status) {
			return nil
		}
		if status == "expired" && current.StartedMS != 0 {
			return store.ErrRetryable
		}
		current.Status = status
		current.Error = cause.Error()
		current.ProcessedMS = s.Store.CurrentTime().UnixMilli()
		current.Version = doc.Version + 1
		if execution, err := tx.Get("execution", op.ExecutionID); err == nil {
			req, err := store.Decode[model.Execution](execution)
			if err != nil {
				return err
			}
			current.ResultVersion = execution.Version
			current.Result = &req
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if _, err = tx.Put("control_operation", op.ID, doc.Version, current); err != nil {
			return err
		}
		if err = tx.Audit(op.Actor, "control.operation."+status, op.ExecutionID, op.ExecutionID, current); err != nil {
			return err
		}
		if op.OriginNodeID != s.NodeID {
			return tx.Enqueue(fmt.Sprintf("control-operation-receipt:%s:%d", op.ID, current.Version), "cloud_operation_receipt", s.NodeID, current)
		}
		return nil
	})
}

func (s *Service) ProcessOperation(ctx context.Context, envelope model.ControlOperationEnvelope) error {
	op := envelope.Operation
	if !s.Edge || op.ID == "" || op.ExecutionID != envelope.Execution.DownlinkID || op.TargetNodeID != s.NodeID || !has([]string{"cancel", "reconcile", "resume"}, op.Action) {
		return errors.New("invalid edge control operation")
	}
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		if doc, err := tx.Get("control_operation", op.ID); err == nil {
			old, err := store.Decode[model.ControlOperation](doc)
			if err != nil {
				return err
			}
			if old.RequestHash != op.RequestHash || old.ExecutionID != op.ExecutionID || old.Actor.UserID != op.Actor.UserID {
				return store.ErrConflict
			}
			op = old
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		op.Version = 1
		op.Status = "pending"
		op.Result = nil
		op.ResultVersion = 0
		op.ProcessedMS = 0
		op.StartedMS = 0
		_, err := tx.Put("control_operation", op.ID, 0, op)
		if err != nil {
			return err
		}
		return tx.Audit(op.Actor, "control.operation.received", envelope.Execution.DefinitionID, op.ExecutionID, op)
	})
	if err != nil {
		return err
	}
	if operationFinished(op.Status) {
		return nil
	}
	op, err = s.claimOperation(ctx, op.ID)
	if errors.Is(err, errOperationExpired) {
		return s.rejectOperation(ctx, op, "expired", err)
	}
	if err != nil {
		return err
	}
	if operationFinished(op.Status) {
		return nil
	}
	startedMS := op.StartedMS
	if err = s.fault(ctx, "after_operation_started", envelope.Execution, op.ID); err != nil {
		return err
	}
	req, err := s.Get(ctx, op.ExecutionID)
	p := identity.Principal{User: model.User{ID: op.Actor.UserID}, Actor: op.Actor}
	if errors.Is(err, store.ErrNotFound) && op.ExpectedSourceVersion == 0 && op.Action == "cancel" {
		p, _, access, authErr := s.manualAccess(ctx, p, envelope.Execution, false)
		if authErr != nil {
			return s.rejectOperation(ctx, op, "rejected", authErr)
		}
		cancelled := envelope.Execution
		cancelled.Version = 0
		cancelled.Status = "cancelled"
		cancelled.CancelRequestedMS = s.Store.CurrentTime().UnixMilli()
		cancelled.CancelActor = &p.Actor
		cancelled.CancelReason = op.Reason
		cancelled.Reason = op.Reason
		operationCtx := context.WithValue(ctx, operationKey{}, processingOperation{ID: op.ID, StartedMS: startedMS})
		err = s.Store.Write(operationCtx, func(tx *store.Tx) error {
			if err := tx.CheckRevisions(access.revisions); err != nil {
				return err
			}
			opts := s.transitionOptions(cancelled)
			opts.Actor = p.Actor
			opts.Source = "cloud_operation"
			if err := ApplyTransition(tx, &cancelled, 0, "cancel_before_start", opts); err != nil {
				return err
			}
			return s.finishOperationTx(operationCtx, tx, cancelled)
		})
		if err != nil {
			return s.rejectOperation(ctx, op, "rejected", err)
		}
		return nil
	}
	if err != nil {
		return s.rejectOperation(ctx, op, "rejected", err)
	}
	if req.Version != op.ExpectedSourceVersion || !sameRequest(req, envelope.Execution) {
		return s.rejectOperation(ctx, op, "rejected", store.ErrConflict)
	}
	operationCtx := context.WithValue(ctx, operationKey{}, processingOperation{ID: op.ID, StartedMS: startedMS})
	input := model.ExecutionAction{ExpectedVersion: req.Version, Reason: op.Reason}
	switch op.Action {
	case "cancel":
		_, err = s.Cancel(operationCtx, p, op.ExecutionID, input)
	case "reconcile":
		_, err = s.Reconcile(operationCtx, p, op.ExecutionID, input)
	case "resume":
		_, err = s.Resume(operationCtx, p, op.ExecutionID, input)
	}
	if err != nil {
		// An already committed outcome remains completed when a subsequent
		// coordination checkpoint reports an outage; its durable work can resume.
		if current, e := s.operation(ctx, op.ID); e == nil && operationFinished(current.Status) {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, store.ErrRetryable) || errors.Is(err, ErrLeaseHeld) {
			return err
		}
		return s.rejectOperation(ctx, op, "rejected", err)
	}
	return nil
}

func (s *Service) appendOperationDetail(ctx context.Context, out *model.ExecutionDetail, d model.Definition) error {
	node, version, err := s.sourceRevision(ctx, out.Execution, d)
	if err != nil {
		return err
	}
	out.SourceNodeID = node
	out.SourceVersion = version
	out.Operations = []model.ControlOperation{}
	docs, err := s.Store.List(ctx, "control_operation")
	if err != nil {
		return err
	}
	for _, doc := range docs {
		op, err := store.Decode[model.ControlOperation](doc)
		if err != nil {
			return err
		}
		if op.ExecutionID != out.Execution.DownlinkID {
			continue
		}
		op.Version = doc.Version
		out.Operations = append(out.Operations, op)
		out.Timeline = append(out.Timeline, model.ExecutionTimelineEntry{ID: "operation-request:" + op.ID, Kind: "operation_request", Source: "control_operation:" + op.OriginNodeID, AtMS: op.RequestedMS, Actor: op.Actor, TraceID: op.TraceID, Message: op.Action + ": " + op.Reason})
		if op.StartedMS > 0 {
			out.Timeline = append(out.Timeline, model.ExecutionTimelineEntry{ID: "operation-start:" + op.ID, Kind: "operation_started", Source: "control_operation:" + op.TargetNodeID, AtMS: op.StartedMS, Actor: op.Actor, TraceID: op.TraceID, Message: op.Action + " started"})
		}
		if operationFinished(op.Status) {
			out.Timeline = append(out.Timeline, model.ExecutionTimelineEntry{ID: "operation-result:" + op.ID, Kind: "operation_result", Source: "control_operation:" + op.TargetNodeID, AtMS: op.ProcessedMS, Actor: op.Actor, TraceID: op.TraceID, Message: strings.TrimSpace(op.Action + " " + op.Status + " " + op.Error)})
		}
	}
	return nil
}

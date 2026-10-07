package control

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"go.opentelemetry.io/otel/trace"
)

func (s *Service) fault(ctx context.Context, phase string, req model.Execution, command string) error {
	if s.Fault != nil {
		return s.Fault(ctx, phase, req, command)
	}
	return nil
}

func (s *Service) checkpoint(ctx context.Context, req model.Execution) error {
	if req.Fence > 0 && req.CoordinatorID == s.NodeID {
		if journal, ok := s.Coordinator.(ExecutionCoordinator); ok {
			return journal.Checkpoint(ctx, req)
		}
		return errors.New("execution fencing is unavailable")
	}
	return nil
}

func (s *Service) save(ctx context.Context, req *model.Execution, event string) error {
	err := s.saveTransition(saveRequest{Context: ctx, Execution: req, Event: event, Options: s.transitionOptions(*req)})
	if err == nil {
		err = s.checkpoint(ctx, *req)
	}
	return err
}

func unknownActions(actions []store.ControlAction) bool {
	for _, a := range actions {
		if a.Status == "reserved" || a.Status == "RESULT_UNKNOWN" {
			return true
		}
	}
	return false
}

func (s *Service) reject(ctx context.Context, req model.Execution, check Check) (model.Execution, error) {
	req.Status = "rejected"
	req.Reason = strings.Join(check.Reasons, "; ")
	if check.Interlocked {
		req.Reason = "physical interlock: " + req.Reason
	}
	req.Snapshot = check.Snapshot
	if actions, err := s.Store.ExecutionActions(ctx, req.DownlinkID); err != nil {
		return req, err
	} else if unknownActions(actions) {
		req.Status = "result_unknown"
	}
	err := s.save(ctx, &req, "reject")
	return req, err
}

func (s *Service) Run(ctx context.Context, supplied model.Execution, automatic bool) (_ model.Execution, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "control.run", observability.Identity{RequestID: supplied.DownlinkID, ActorID: supplied.Actor.UserID, DefinitionID: supplied.DefinitionID, EntityRevision: strconv.FormatInt(supplied.DefinitionVersion, 10)})
	defer func() { finish(operationErr) }()
	waitCtx, waitFinish := observability.StartOperation(ctx, "control.wait", observability.Identity{RequestID: supplied.DownlinkID})
	releaseBudget, err := s.Store.AcquireControl(waitCtx)
	waitFinish(err)
	if err != nil {
		return supplied, err
	}
	defer releaseBudget()
	if !s.Edge {
		return supplied, errors.New("device execution is only available on an edge")
	}
	if supplied.DownlinkID == "" {
		return supplied, errors.New("execution identifier is required")
	}
	req := supplied
	old, err := s.Get(ctx, req.DownlinkID)
	exists := err == nil
	if exists {
		if old.DefinitionID != supplied.DefinitionID || old.DefinitionVersion != supplied.DefinitionVersion || old.Override != supplied.Override || store.Hash(old.Params) != store.Hash(supplied.Params) || supplied.Binding != "" && old.Binding != supplied.Binding {
			return old, store.ErrConflict
		}
		req = old
		if terminal(req.Status) {
			return req, nil
		}
		if isRunning(req.Status) {
			if err = s.fault(ctx, "process_recovery", req, req.ActiveCommandID); err != nil {
				return req, err
			}
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return req, err
	}
	fromJournal := false
	if !exists && req.Status != "queued" {
		journal, ok := s.Coordinator.(ExecutionCoordinator)
		if !ok || req.Fence == 0 {
			return req, ErrTransition
		}
		verified, recoveryErr := journal.Recover(ctx, req.DownlinkID)
		if recoveryErr != nil {
			return req, recoveryErr
		}
		if !sameRequest(req, verified) {
			return req, store.ErrConflict
		}
		req = verified
		req.Status = "queued"
		req.Fence = 0
		req.CoordinatorID = ""
		req.Steps = nil
		req.ActiveCommandID = ""
		fromJournal = true
	}
	if automatic && (req.Actor.UserID != "published-policy" || req.Override) {
		return req, identity.ErrDenied
	}
	if req.Mode == "" {
		req.Mode = "manual"
		if automatic || req.Actor.UserID == "published-policy" {
			req.Mode = "automatic"
		}
	}
	if !has([]string{"manual", "automatic", "scheduled"}, req.Mode) {
		return req, errors.New("unknown execution mode")
	}
	d, definitionErr := s.Definitions.Published(ctx, req.DefinitionID, 0)
	if !exists {
		req.Version = 0
		if req.CreatedMS == 0 {
			req.CreatedMS = s.Store.CurrentTime().UnixMilli()
		}
		if req.Mode != "manual" && req.Binding == "" && !fromJournal {
			req.Binding = store.Hash([]any{req.DefinitionID, req.DefinitionVersion, req.Params, req.Override, req.Mode})
		}
		if definitionErr == nil {
			access, err := s.executionAccess(ctx, req, d)
			if err != nil {
				return req, err
			}
			if req.ResourceBinding == "" {
				req.ResourceBinding = access.binding()
			}
			if err = verifyResourceBinding(req, access); err != nil {
				return req, err
			}
			err = s.saveTransition(saveRequest{Context: ctx, Execution: &req, Event: "accept", Options: s.transitionOptions(req), Revisions: access.revisions})
			if err != nil {
				return req, err
			}
		} else {
			return req, definitionErr
		}
	}
	if definitionErr != nil {
		return s.reject(ctx, req, Check{Reasons: []string{"published policy unavailable: " + definitionErr.Error()}})
	}
	if req.Status != "queued" && !isRunning(req.Status) {
		return req, ErrTransition
	}
	var coordinationFailure error
	if journal, ok := s.Coordinator.(ExecutionCoordinator); ok && (len(d.Policy.EdgeIDs) > 1 || req.Fence > 0) {
		if recovered, recoveryErr := journal.Recover(ctx, req.DownlinkID); recoveryErr == nil {
			if !sameRequest(req, recovered) {
				return req, store.ErrConflict
			}
			if recovered.Fence > req.Fence || recovered.Fence == req.Fence && recovered.Version > req.Version {
				version := req.Version
				req = recovered
				req.Version = version
				if err = s.save(ctx, &req, "checkpoint_recovered"); err != nil {
					return req, err
				}
				if terminal(req.Status) {
					return req, nil
				}
			}
		} else if isRunning(req.Status) && !errors.Is(recoveryErr, store.ErrNotFound) {
			coordinationFailure = fmt.Errorf("cannot reconcile the in-progress execution: %w", recoveryErr)
		}
	}
	if d.Status != "published" || d.Version != req.DefinitionVersion {
		return s.reject(ctx, req, Check{Reasons: []string{"published policy version changed"}})
	}
	if !isRunning(req.Status) && s.Store.CurrentTime().UnixMilli() >= req.StartDeadlineMS {
		return s.reject(ctx, req, Check{Reasons: []string{"execution start deadline expired"}})
	}
	if req.Mode == "manual" && !isRunning(req.Status) {
		if err = s.validateApprovals(ctx, req); err != nil {
			return s.reject(ctx, req, Check{Reasons: []string{err.Error()}})
		}
	}
	access, err := s.executionAccess(ctx, req, d)
	if err != nil {
		return s.reject(ctx, req, Check{Reasons: []string{err.Error()}})
	}
	if err = verifyResourceBinding(req, access); err != nil {
		return s.reject(ctx, req, Check{Reasons: []string{err.Error()}})
	}
	if req.ResourceBinding == "" {
		req.ResourceBinding = access.binding()
	}
	check, err := s.Conditions(ctx, d)
	if err != nil {
		return req, err
	}
	steps := d.Policy.Steps
	release := func() {}
	degraded := req.Branch == "degraded" || req.Status == "degraded_running"
	if degraded {
		steps = d.Policy.Degraded
	}
	if len(d.Policy.EdgeIDs) > 1 && !degraded {
		if coordinationFailure != nil {
			err = coordinationFailure
		} else if s.Coordinator == nil {
			err = errors.New("coordination service is unavailable")
		} else if err = s.Coordinator.Ready(ctx, d); err == nil {
			req.Fence, release, err = s.Coordinator.Acquire(ctx, req.DownlinkID, s.NodeID, time.Minute)
			if err == nil {
				req.CoordinatorID = s.NodeID
			}
		}
		if err != nil {
			if errors.Is(err, ErrLeaseHeld) {
				return req, err
			}
			actions, actionErr := s.Store.ExecutionActions(ctx, req.DownlinkID)
			if actionErr != nil {
				return req, actionErr
			}
			if len(actions) > 0 {
				return s.reject(ctx, req, Check{Reasons: []string{"coordination unavailable during an existing action sequence: " + err.Error()}})
			}
			steps = d.Policy.Degraded
			degraded = true
			req.Fence = 0
			req.CoordinatorID = ""
			req.Reason = "configured degraded branch: " + err.Error()
			if len(steps) == 0 {
				return s.reject(ctx, req, Check{Reasons: []string{req.Reason}})
			}
		}
	}
	if release != nil {
		defer release()
	}
	if degraded {
		local, localErr := s.localDefinition(ctx, d, "")
		if localErr != nil {
			return req, localErr
		}
		check, err = s.Conditions(ctx, local)
		if err != nil {
			return req, err
		}
	}
	if check.Interlocked || !check.Allowed && !req.Override {
		return s.reject(ctx, req, check)
	}
	req.Status = "running"
	req.Branch = "normal"
	if degraded {
		req.Status = "degraded_running"
		req.Branch = "degraded"
	}
	req.Snapshot = check.Snapshot
	err = s.saveTransition(saveRequest{Context: ctx, Execution: &req, Event: "start", Options: s.transitionOptions(req), Revisions: access.revisions})
	if err != nil {
		return req, err
	}
	if err = s.checkpoint(ctx, req); err != nil {
		return req, err
	}
	for _, step := range steps {
		latest, err := s.Get(ctx, req.DownlinkID)
		if err != nil {
			return req, err
		}
		if !isRunning(latest.Status) {
			return latest, nil
		}
		req = latest
		completed := false
		for _, r := range req.Steps {
			if r.StepID == step.ID && r.Status == "SUCCESS" {
				completed = true
				break
			}
		}
		if completed || degraded && step.EdgeID != s.NodeID {
			continue
		}
		if s.Dispatcher == nil {
			return req, errors.New("device dispatcher is not configured")
		}
		step.Params = bindParams(step.Params, req.Params)
		result, err := s.runStep(ctx, &req, step, req.DownlinkID+":"+step.ID)
		if err != nil {
			return req, err
		}
		if !isRunning(req.Status) {
			return req, nil
		}
		if result.Status != "SUCCESS" {
			req.Status = "failed"
			if result.Status == "RESULT_UNKNOWN" {
				req.Status = "result_unknown"
			}
			req.Reason = result.Message
			break
		}
	}
	if isRunning(req.Status) {
		req.Status = "completed"
		if degraded {
			req.Status = "degraded_completed"
		}
	}
	err = s.save(ctx, &req, "finish")
	if errors.Is(err, store.ErrConflict) {
		if current, getErr := s.Get(ctx, req.DownlinkID); getErr == nil && terminal(current.Status) {
			return current, nil
		}
	}
	return req, err
}

func (s *Service) localDefinition(ctx context.Context, d model.Definition, deviceID string) (model.Definition, error) {
	local := d
	local.Policy.Conditions = nil
	for _, condition := range d.Policy.Conditions {
		owned := condition.DeviceID == deviceID
		doc, err := s.Store.Get(ctx, "entity", condition.DeviceID)
		if err == nil {
			entity, err := store.Decode[model.Entity](doc)
			if err != nil {
				return local, err
			}
			owned = entity.EdgeID == s.NodeID
		} else if !errors.Is(err, store.ErrNotFound) {
			return local, err
		}
		if owned {
			local.Policy.Conditions = append(local.Policy.Conditions, condition)
		}
	}
	return local, nil
}

func (s *Service) beforeStep(ctx context.Context, req model.Execution, step model.Step) (DispatchResult, error) {
	current, err := s.Get(ctx, req.DownlinkID)
	if err != nil {
		return DispatchResult{}, err
	}
	if !isRunning(current.Status) {
		return DispatchResult{Status: "REJECTED", Message: "execution was cancelled before this action"}, nil
	}
	d, err := s.Definitions.Published(ctx, req.DefinitionID, 0)
	if err != nil {
		return DispatchResult{Status: "REJECTED", Message: err.Error()}, nil
	}
	if d.Version != req.DefinitionVersion {
		return DispatchResult{Status: "REJECTED", Message: "published policy version changed"}, nil
	}
	access, err := s.executionAccess(ctx, req, d)
	if err == nil {
		err = verifyResourceBinding(req, access)
	}
	if err != nil {
		return DispatchResult{Status: "REJECTED", Message: err.Error()}, nil
	}
	if step.EdgeID == s.NodeID {
		local, err := s.localDefinition(ctx, d, step.DeviceID)
		if err != nil {
			return DispatchResult{}, err
		}
		check, err := s.Conditions(ctx, local)
		if err != nil {
			return DispatchResult{}, err
		}
		if check.Interlocked || !check.Allowed && !req.Override {
			return DispatchResult{Status: "REJECTED", Message: strings.Join(check.Reasons, "; ")}, nil
		}
	}
	if err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.CheckRevisions(access.revisions); err != nil {
			return err
		}
		doc, err := tx.Get("execution", req.DownlinkID)
		if err != nil {
			return err
		}
		if doc.Version != req.Version {
			return store.ErrConflict
		}
		return nil
	}); err != nil {
		return DispatchResult{Status: "REJECTED", Message: err.Error()}, nil
	}
	if req.Fence > 0 {
		coordinator, ok := s.Coordinator.(ExecutionCoordinator)
		if !ok {
			return DispatchResult{Status: "REJECTED", Message: "execution fencing is unavailable"}, nil
		}
		if err = coordinator.Validate(ctx, req.DownlinkID, req.CoordinatorID, req.Fence); err != nil {
			return DispatchResult{Status: "REJECTED", Message: err.Error()}, nil
		}
	}
	return DispatchResult{}, nil
}

func (s *Service) runStep(ctx context.Context, req *model.Execution, step model.Step, id string) (_ model.StepResult, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "control.action", observability.Identity{RequestID: req.DownlinkID, CommandID: id, DefinitionID: req.DefinitionID, EntityID: step.DeviceID, EntityRevision: strconv.FormatInt(req.DefinitionVersion, 10)})
	defer func() { finish(operationErr) }()
	r := model.StepResult{StepID: step.ID, CommandID: id, StartedMS: s.Store.CurrentTime().UnixMilli(), Status: "RESULT_UNKNOWN", Message: "action reserved"}
	if err := s.fault(ctx, "before_reserve", *req, id); err != nil {
		return r, err
	}
	if req.Fence > 0 {
		c, ok := s.Coordinator.(ExecutionCoordinator)
		if !ok {
			return r, errors.New("execution fencing is unavailable")
		}
		if err := c.Validate(ctx, req.DownlinkID, req.CoordinatorID, req.Fence); err != nil {
			return r, err
		}
	}
	hash := store.Hash(step)
	execute := false
	updated := *req
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		doc, err := tx.Get("execution", req.DownlinkID)
		if err != nil {
			return err
		}
		current, err := store.Decode[model.Execution](doc)
		if err != nil {
			return err
		}
		if doc.Version != req.Version || !isRunning(current.Status) {
			return store.ErrConflict
		}
		action, created, err := tx.ReserveControlAction(store.ControlAction{CommandID: id, PayloadHash: hash, Fence: req.Fence, Result: r})
		if err != nil {
			return err
		}
		r = action.Result
		execute = created
		if !created {
			if action.Status == "reserved" {
				r.Status = "RESULT_UNKNOWN"
				r.Message = "previous process stopped after reserving the action; reconcile its command result"
			}
			return nil
		}
		updated.ActiveCommandID = id
		opts := s.transitionOptions(updated)
		opts.CommandID = id
		return ApplyTransition(tx, &updated, req.Version, "action_reserved", opts)
	})
	if err != nil {
		return r, err
	}
	*req = updated
	if execute {
		if err = s.fault(ctx, "after_reserve", *req, id); err != nil {
			return r, err
		}
		if err = s.checkpoint(ctx, *req); err != nil {
			return r, err
		}
		timeout := step.TimeoutMS
		if timeout <= 0 {
			timeout = 5000
		}
		runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		defer cancel()
		runCtx = context.WithValue(runCtx, executionKey{}, *req)
		result, sendErr := s.beforeStep(runCtx, *req, step)
		if sendErr == nil && result.Status == "" {
			result, sendErr = s.Dispatcher.Send(runCtx, step, id, s.Store.CurrentTime().Add(time.Duration(timeout)*time.Millisecond).UnixMilli())
			if err = s.fault(ctx, "after_send", *req, id); err != nil {
				return r, err
			}
		}
		r.FinishedMS = s.Store.CurrentTime().UnixMilli()
		r.Status = result.Status
		r.Message = result.Message
		if sendErr != nil {
			r.Status = "RESULT_UNKNOWN"
			r.Message = sendErr.Error()
		}
		if r.Status == "" {
			r.Status = "RESULT_UNKNOWN"
			r.Message = "device did not return a business result"
		}
		if err = s.fault(ctx, "before_result_save", *req, id); err != nil {
			return r, err
		}
	}
	if err = s.recordStep(ctx, req, step, hash, r); err != nil {
		return r, err
	}
	if err = s.fault(ctx, "after_result_save", *req, id); err != nil {
		return r, err
	}
	return r, nil
}

func (s *Service) recordStep(ctx context.Context, req *model.Execution, step model.Step, hash string, result model.StepResult) error {
	updated := *req
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		doc, err := tx.Get("execution", req.DownlinkID)
		if err != nil {
			return err
		}
		current, err := store.Decode[model.Execution](doc)
		if err != nil {
			return err
		}
		if !sameRequest(current, *req) {
			return store.ErrConflict
		}
		priorAction, err := tx.ControlAction(result.CommandID)
		if err != nil {
			return err
		}
		evidenceIDs := []string{}
		if current.CancelRequestedMS > 0 && (priorAction.Status == "reserved" || priorAction.Status == "RESULT_UNKNOWN") && has([]string{"SUCCESS", "FAILURE", "REJECTED"}, result.Status) {
			evidence := model.CommandEvidence{ID: identity.ID(), ExecutionID: current.DownlinkID, CommandID: result.CommandID, DeviceID: step.DeviceID, StepID: step.ID, Source: "control.action_journal:" + s.NodeID, ObservedMS: result.FinishedMS, CollectedMS: s.Store.CurrentTime().UnixMilli(), PayloadHash: hash, RequestHash: hash, ContentHash: store.Hash(result), Status: result.Status, Message: result.Message, Trusted: true, Actor: current.Actor}
			if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
				evidence.TraceID = sc.TraceID().String()
			}
			if err = tx.SaveControlEvidence(evidence); err != nil {
				return err
			}
			result.EvidenceID = evidence.ID
			evidenceIDs = append(evidenceIDs, evidence.ID)
		}
		if err = tx.FinishControlAction(result.CommandID, hash, result); err != nil {
			return err
		}
		storedAction, err := tx.ControlAction(result.CommandID)
		if err != nil {
			return err
		}
		current.Version = doc.Version
		current.Steps = replaceStep(current.Steps, storedAction.Result)
		if current.ActiveCommandID == result.CommandID {
			current.ActiveCommandID = ""
		}
		if current.Status == "cancelled_result_unknown" {
			actions, err := tx.ExecutionActions(req.DownlinkID)
			if err != nil {
				return err
			}
			if !unknownActions(actions) {
				current.Status = "cancelled"
			}
		}
		opts := s.transitionOptions(current)
		opts.CommandID = result.CommandID
		opts.EvidenceIDs = evidenceIDs
		if err = ApplyTransition(tx, &current, doc.Version, "step", opts); err != nil {
			return err
		}
		updated = current
		return nil
	})
	if err == nil {
		*req = updated
		err = s.checkpoint(ctx, updated)
	}
	return err
}

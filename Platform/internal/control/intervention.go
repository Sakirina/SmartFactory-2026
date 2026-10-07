package control

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"go.opentelemetry.io/otel/trace"
)

func validateIntervention(input model.ExecutionAction) error {
	if input.ExpectedVersion < 1 {
		return store.ErrConflict
	}
	if strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 8000 {
		return errors.New("a reason of at most 2000 characters is required")
	}
	return nil
}

func (s *Service) manualAccess(ctx context.Context, p identity.Principal, req model.Execution, requireVersion bool) (identity.Principal, model.Definition, *accessSnapshot, error) {
	d, err := s.Definitions.Published(ctx, req.DefinitionID, 0)
	if err != nil {
		return p, d, nil, err
	}
	p, access, err := s.authorize(ctx, p, d, "control")
	if err != nil {
		return p, d, access, err
	}
	if requireVersion {
		if d.Version != req.DefinitionVersion {
			return p, d, access, store.ErrConflict
		}
		if err = verifyResourceBinding(req, access); err != nil {
			return p, d, access, err
		}
	} else if d.Version != req.DefinitionVersion {
		original, e := s.Definitions.Version(ctx, req.DefinitionID, req.DefinitionVersion)
		if e != nil {
			return p, d, access, e
		}
		if e = s.permitResource(ctx, p, "control", original.GroupID, access); e != nil {
			return p, d, access, e
		}
		readResources := append([]string{}, original.Selector.DeviceIDs...)
		if original.Selector.AssetID != "" {
			readResources = append(readResources, original.Selector.AssetID)
		}
		for _, condition := range original.Policy.Conditions {
			readResources = append(readResources, condition.DeviceID)
		}
		for _, sample := range req.Snapshot {
			readResources = append(readResources, sample.DeviceID)
		}
		for _, resource := range readResources {
			if e = s.permitResource(ctx, p, "read", resource, access); e != nil {
				return p, d, access, e
			}
		}
		for _, id := range original.Dependencies {
			dependency, e := s.Definitions.Published(ctx, id, 0)
			if e != nil {
				return p, d, access, e
			}
			if e = s.definitionAccess(ctx, p, dependency, "read", access); e != nil {
				return p, d, access, e
			}
		}
		for _, steps := range [][]model.Step{original.Policy.Steps, original.Policy.Degraded} {
			for _, step := range steps {
				if e = s.permitResource(ctx, p, "control", step.DeviceID, access); e != nil {
					return p, d, access, e
				}
			}
		}
		d = original
	}
	return p, d, access, nil
}

func (s *Service) manualAuthority(ctx context.Context, req *model.Execution, d model.Definition) (func(), error) {
	if req.Fence == 0 && len(d.Policy.EdgeIDs) <= 1 {
		return func() {}, nil
	}
	c, ok := s.Coordinator.(ExecutionCoordinator)
	if !ok {
		return nil, errors.New("current execution authority is unavailable")
	}
	if isRunning(req.Status) && req.CoordinatorID == s.NodeID {
		if err := c.Validate(ctx, req.DownlinkID, req.CoordinatorID, req.Fence); err == nil {
			return func() {}, nil
		}
	}
	fence, release, err := c.Acquire(ctx, req.DownlinkID, s.NodeID, time.Minute)
	if err != nil {
		return nil, err
	}
	if fence <= req.Fence {
		release()
		return nil, errors.New("manual intervention requires a newer execution fence")
	}
	if err = c.Validate(ctx, req.DownlinkID, s.NodeID, fence); err != nil {
		release()
		return nil, err
	}
	req.Fence = fence
	req.CoordinatorID = s.NodeID
	return release, nil
}

func (s *Service) Cancel(ctx context.Context, p identity.Principal, id string, input model.ExecutionAction) (_ model.Execution, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "control.cancel", observability.Identity{ActorID: p.User.ID, RequestID: id})
	defer func() { finish(operationErr) }()
	if err := validateIntervention(input); err != nil {
		return model.Execution{}, err
	}
	req, err := s.Get(ctx, id)
	if err != nil {
		return req, err
	}
	if req.Version != input.ExpectedVersion {
		return req, store.ErrConflict
	}
	p, d, access, err := s.manualAccess(ctx, p, req, false)
	if err != nil {
		return req, err
	}
	if _, ok := transitionTable["cancel"][req.Status]; !ok {
		return req, ErrTransition
	}
	release, err := s.manualAuthority(ctx, &req, d)
	if err != nil {
		return req, err
	}
	defer release()
	updated := req
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.CheckRevisions(access.revisions); err != nil {
			return err
		}
		doc, err := tx.Get("execution", id)
		if err != nil {
			return err
		}
		if doc.Version != input.ExpectedVersion {
			return store.ErrConflict
		}
		actions, err := tx.ExecutionActions(id)
		if err != nil {
			return err
		}
		updated.Status = "cancelled"
		if unknownActions(actions) || req.Status == "result_unknown" || req.ActiveCommandID != "" {
			updated.Status = "cancelled_result_unknown"
		}
		updated.CancelRequestedMS = s.Store.CurrentTime().UnixMilli()
		updated.CancelActor = &p.Actor
		updated.CancelReason = input.Reason
		updated.Reason = input.Reason
		opts := s.transitionOptions(updated)
		opts.Actor = p.Actor
		if err = ApplyTransition(tx, &updated, input.ExpectedVersion, "cancel", opts); err != nil {
			return err
		}
		return s.finishOperationTx(ctx, tx, updated)
	})
	if err == nil {
		req = updated
		err = s.checkpoint(ctx, req)
	}
	return req, err
}

func executionSteps(d model.Definition, req model.Execution) []model.Step {
	if req.Branch == "degraded" || req.Status == "degraded_running" || req.Status == "degraded_completed" {
		return d.Policy.Degraded
	}
	return d.Policy.Steps
}
func stepForAction(d model.Definition, req model.Execution, id string) (model.Step, error) {
	for _, steps := range [][]model.Step{executionSteps(d, req), d.Policy.Degraded} {
		for _, step := range steps {
			if req.DownlinkID+":"+step.ID == id {
				step.Params = bindParams(step.Params, req.Params)
				return step, nil
			}
		}
	}
	return model.Step{}, errors.New("command does not belong to the published execution")
}
func (s *Service) actionsForExecution(ctx context.Context, req model.Execution, d model.Definition) ([]store.ControlAction, error) {
	actions, err := s.Store.ExecutionActions(ctx, req.DownlinkID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, a := range actions {
		seen[a.CommandID] = true
	}
	results := append([]model.StepResult{}, req.Steps...)
	if req.ActiveCommandID != "" && !seen[req.ActiveCommandID] {
		step, err := stepForAction(d, req, req.ActiveCommandID)
		if err != nil {
			return nil, err
		}
		results = append(results, model.StepResult{CommandID: req.ActiveCommandID, StepID: step.ID, Status: "RESULT_UNKNOWN", StartedMS: req.CreatedMS})
	}
	for _, r := range results {
		if seen[r.CommandID] {
			continue
		}
		seen[r.CommandID] = true
		step, err := stepForAction(d, req, r.CommandID)
		if err != nil {
			return nil, err
		}
		actions = append(actions, store.ControlAction{CommandID: r.CommandID, PayloadHash: store.Hash(step), Status: r.Status, Fence: req.Fence, Result: r, UpdatedMS: r.FinishedMS})
	}
	return actions, nil
}

func (s *Service) Reconcile(ctx context.Context, p identity.Principal, id string, input model.ExecutionAction) (_ model.Execution, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "control.reconcile", observability.Identity{ActorID: p.User.ID, RequestID: id})
	defer func() { finish(operationErr) }()
	if !s.Edge {
		return model.Execution{}, errors.New("command results are reconciled by the owning edge")
	}
	if err := validateIntervention(input); err != nil {
		return model.Execution{}, err
	}
	req, err := s.Get(ctx, id)
	if err != nil {
		return req, err
	}
	if req.Version != input.ExpectedVersion {
		return req, store.ErrConflict
	}
	if req.Status != "result_unknown" && req.Status != "cancelled_result_unknown" {
		return req, ErrTransition
	}
	p, d, access, err := s.manualAccess(ctx, p, req, false)
	if err != nil {
		return req, err
	}
	release, err := s.manualAuthority(ctx, &req, d)
	if err != nil {
		return req, err
	}
	defer release()
	query, ok := s.Dispatcher.(ResultQuerier)
	if !ok {
		return req, errors.New("read-only command result query is unavailable")
	}
	actions, err := s.actionsForExecution(ctx, req, d)
	if err != nil {
		return req, err
	}
	evidence := []model.CommandEvidence{}
	results := map[string]model.StepResult{}
	for _, action := range actions {
		alreadyKnown := false
		for _, r := range req.Steps {
			if r.CommandID == action.CommandID && r.Status == action.Status && r.Status != "RESULT_UNKNOWN" {
				alreadyKnown = true
			}
		}
		if action.Status != "reserved" && action.Status != "RESULT_UNKNOWN" && alreadyKnown {
			continue
		}
		step, err := stepForAction(d, req, action.CommandID)
		if err != nil {
			return req, err
		}
		if store.Hash(step) != action.PayloadHash {
			return req, store.ErrConflict
		}
		queryCtx := context.WithValue(ctx, executionKey{}, req)
		queryCtx, queryFinish := observability.StartOperation(queryCtx, "control.result_lookup", observability.Identity{ActorID: p.User.ID, RequestID: req.DownlinkID, CommandID: action.CommandID, DefinitionID: req.DefinitionID, EntityID: step.DeviceID, EntityRevision: strconv.FormatInt(req.DefinitionVersion, 10)})
		feedback, queryErr := query.LookupCommand(queryCtx, step, action.CommandID)
		queryFinish(queryErr)
		now := s.Store.CurrentTime().UnixMilli()
		item := model.CommandEvidence{ID: identity.ID(), ExecutionID: id, CommandID: action.CommandID, DeviceID: step.DeviceID, StepID: step.ID, Source: feedback.Source, ObservedMS: feedback.ObservedMS, CollectedMS: now, PayloadHash: action.PayloadHash, ContentHash: feedback.ContentHash, RequestHash: feedback.RequestHash, Status: feedback.Status, Message: feedback.Message, Actor: p.Actor}
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			item.TraceID = sc.TraceID().String()
		}
		maxAge := s.Store.Policy().ApprovalTTLMS
		if maxAge <= 0 {
			maxAge = 300000
		}
		switch {
		case queryErr != nil:
			item.Rejection = queryErr.Error()
		case !feedback.Found:
			item.Status = "NOT_FOUND"
			item.Rejection = "command result is not present"
		case !feedback.Bound || feedback.CommandID != action.CommandID || feedback.DeviceID != step.DeviceID || feedback.PayloadHash != action.PayloadHash:
			item.Rejection = "feedback command, device or request content does not match"
		case feedback.Source == "" || feedback.ContentHash == "":
			item.Rejection = "feedback provenance is incomplete"
		case feedback.ObservedMS <= 0 || feedback.ObservedMS < action.Result.StartedMS || feedback.ObservedMS > now+5000 || now-feedback.ObservedMS > maxAge:
			item.Rejection = "feedback is outside the command time and freshness window"
		case !has([]string{"SUCCESS", "FAILURE", "REJECTED"}, feedback.Status):
			item.Rejection = "device result remains unresolved"
		default:
			item.Trusted = true
		}
		if item.Trusted {
			r := action.Result
			r.Status = feedback.Status
			r.Message = feedback.Message
			r.FinishedMS = feedback.ObservedMS
			r.EvidenceID = item.ID
			results[action.CommandID] = r
		}
		evidence = append(evidence, item)
	}
	updated := req
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.CheckRevisions(access.revisions); err != nil {
			return err
		}
		doc, err := tx.Get("execution", id)
		if err != nil {
			return err
		}
		if doc.Version != input.ExpectedVersion {
			return store.ErrConflict
		}
		for _, action := range actions {
			if _, err := tx.ControlAction(action.CommandID); errors.Is(err, store.ErrNotFound) {
				if _, _, err = tx.ReserveControlAction(action); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if result, ok := results[action.CommandID]; ok {
				if err = tx.FinishControlAction(action.CommandID, action.PayloadHash, result); err != nil {
					return err
				}
				updated.Steps = replaceStep(updated.Steps, result)
			}
		}
		ids := []string{}
		for _, item := range evidence {
			if err = tx.SaveControlEvidence(item); err != nil {
				return err
			}
			ids = append(ids, item.ID)
		}
		pending, err := tx.ExecutionActions(id)
		if err != nil {
			return err
		}
		if len(results) > 0 && !unknownActions(pending) {
			updated.ActiveCommandID = ""
			if updated.CancelRequestedMS > 0 || updated.Status == "cancelled_result_unknown" {
				updated.Status = "cancelled"
			} else {
				updated.Status = "ready_to_resume"
				updated.ReconciledMS = s.Store.CurrentTime().UnixMilli()
				for _, a := range pending {
					if a.Result.Status != "SUCCESS" {
						updated.Status = "failed"
					}
				}
				if updated.Status == "ready_to_resume" && allStepsComplete(d, updated, s.NodeID) {
					updated.Status = "completed"
					if updated.Branch == "degraded" {
						updated.Status = "degraded_completed"
					}
				}
			}
		}
		updated.Reason = input.Reason
		opts := s.transitionOptions(updated)
		opts.Actor = p.Actor
		opts.EvidenceIDs = ids
		if err = ApplyTransition(tx, &updated, input.ExpectedVersion, "reconcile", opts); err != nil {
			return err
		}
		return s.finishOperationTx(ctx, tx, updated)
	})
	if err == nil {
		req = updated
		err = s.checkpoint(ctx, req)
	}
	return req, err
}

func allStepsComplete(d model.Definition, req model.Execution, node string) bool {
	for _, step := range executionSteps(d, req) {
		if req.Branch == "degraded" && step.EdgeID != node {
			continue
		}
		found := false
		for _, r := range req.Steps {
			if r.StepID == step.ID && r.CommandID == req.DownlinkID+":"+step.ID && r.Status == "SUCCESS" {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *Service) Resume(ctx context.Context, p identity.Principal, id string, input model.ExecutionAction) (_ model.Execution, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "control.resume", observability.Identity{ActorID: p.User.ID, RequestID: id})
	defer func() { finish(operationErr) }()
	if !s.Edge {
		return model.Execution{}, errors.New("execution recovery is performed by the owning edge")
	}
	if err := validateIntervention(input); err != nil {
		return model.Execution{}, err
	}
	req, err := s.Get(ctx, id)
	if err != nil {
		return req, err
	}
	if req.Version != input.ExpectedVersion {
		return req, store.ErrConflict
	}
	if req.Status != "ready_to_resume" || req.CancelRequestedMS > 0 || req.ReconciledMS == 0 {
		return req, ErrTransition
	}
	p, d, access, err := s.manualAccess(ctx, p, req, true)
	if err != nil {
		return req, err
	}
	executionAccess, err := s.executionAccess(ctx, req, d)
	if err != nil {
		return req, err
	}
	access.revisions = append(access.revisions, executionAccess.revisions...)
	if req.Mode == "manual" || req.Mode == "" {
		if err = s.validateApprovals(ctx, req); err != nil {
			return req, err
		}
	}
	check, err := s.Conditions(ctx, d)
	if err != nil {
		return req, err
	}
	if check.Interlocked || !check.Allowed && !req.Override {
		return req, errors.New("current conditions or physical interlocks prevent recovery")
	}
	if req.Mode == "manual" && req.Binding != binding(d, req, check) {
		return req, store.ErrConflict
	}
	if s.Coordinator != nil && len(d.Policy.EdgeIDs) > 1 {
		if err = s.Coordinator.Ready(ctx, d); err != nil {
			return req, err
		}
	}
	release, err := s.manualAuthority(ctx, &req, d)
	if err != nil {
		return req, err
	}
	defer release()
	updated := req
	updated.Status = "queued"
	updated.ResumeActor = &p.Actor
	updated.Reason = input.Reason
	updated.StartDeadlineMS = s.Store.CurrentTime().Add(store.Milliseconds(s.Store.Policy().StartTTLMS)).UnixMilli()
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.CheckRevisions(access.revisions); err != nil {
			return err
		}
		actions, err := tx.ExecutionActions(id)
		if err != nil {
			return err
		}
		if unknownActions(actions) {
			return ErrTransition
		}
		opts := s.transitionOptions(updated)
		opts.Actor = p.Actor
		if err = ApplyTransition(tx, &updated, input.ExpectedVersion, "resume", opts); err != nil {
			return err
		}
		if err = s.finishOperationTx(ctx, tx, updated); err != nil {
			return err
		}
		return tx.Enqueue(fmt.Sprintf("resume:%s:%d", id, updated.Version), "edge_downlink", s.NodeID, updated)
	})
	if err == nil {
		req = updated
		err = s.checkpoint(ctx, req)
	}
	return req, err
}

func (s *Service) Detail(ctx context.Context, p identity.Principal, id string) (model.ExecutionDetail, error) {
	req, err := s.Get(ctx, id)
	if err != nil {
		return model.ExecutionDetail{}, err
	}
	d, err := s.executionReadAccess(ctx, p, req)
	if err != nil {
		return model.ExecutionDetail{}, err
	}
	out := model.ExecutionDetail{Execution: req, AllowedActions: []model.ExecutionAllowedAction{}, Timeline: []model.ExecutionTimelineEntry{}}
	out.Evidence, err = s.Store.ExecutionEvidence(ctx, id)
	if err != nil {
		return out, err
	}
	transitions, err := s.Store.ExecutionTransitions(ctx, id)
	if err != nil {
		return out, err
	}
	for _, item := range transitions {
		v := item
		out.Timeline = append(out.Timeline, model.ExecutionTimelineEntry{ID: fmt.Sprintf("transition:%d", v.Version), Kind: "transition", Source: v.Source, AtMS: v.AtMS, Transition: &v, Actor: v.Actor, TraceID: v.TraceID})
	}
	for i, item := range req.Approvals {
		v := item
		out.Timeline = append(out.Timeline, model.ExecutionTimelineEntry{ID: fmt.Sprintf("approval:%d", i), Kind: "approval", Source: "execution.approvals", AtMS: v.AtMS, Approval: &v, Actor: v.Actor})
	}
	actions, err := s.Store.ExecutionActions(ctx, id)
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, a := range actions {
		v := a.Result
		seen[v.CommandID] = true
		out.Timeline = append(out.Timeline, model.ExecutionTimelineEntry{ID: "action:" + v.CommandID, Kind: "action", Source: "action_journal", AtMS: a.UpdatedMS, Step: &v, Actor: req.Actor})
	}
	for _, r := range req.Steps {
		if !seen[r.CommandID] {
			v := r
			out.Timeline = append(out.Timeline, model.ExecutionTimelineEntry{ID: "step:" + v.CommandID, Kind: "action", Source: "execution.steps", AtMS: v.FinishedMS, Step: &v, Actor: req.Actor})
		}
	}
	for _, item := range out.Evidence {
		v := item
		out.Timeline = append(out.Timeline, model.ExecutionTimelineEntry{ID: "evidence:" + v.ID, Kind: "feedback", Source: v.Source, AtMS: v.CollectedMS, Evidence: &v, Actor: v.Actor, TraceID: v.TraceID})
	}
	// Audit is the original time source for records written before migration 5.
	audits, err := s.Store.AuditList(ctx, id, 10000)
	if err != nil {
		return out, err
	}
	for _, a := range audits {
		if !strings.HasPrefix(a.Action, "control.") {
			continue
		}
		if snapshot, ok := a.Snapshot.(map[string]any); ok && snapshot["transition"] != nil {
			continue
		}
		out.Timeline = append(out.Timeline, model.ExecutionTimelineEntry{ID: fmt.Sprintf("audit:%s:%d", a.SourceID, a.Sequence), Kind: "legacy_audit", Source: "audit:" + a.SourceID, AtMS: a.OccurredMS, Actor: a.Actor, Message: a.Action})
	}
	_, _, _, permissionErr := s.manualAccess(ctx, p, req, false)
	for _, action := range []string{"cancel", "reconcile", "resume"} {
		allowed := false
		switch action {
		case "cancel":
			_, allowed = transitionTable["cancel"][req.Status]
		case "reconcile":
			allowed = req.Status == "result_unknown" || req.Status == "cancelled_result_unknown"
		case "resume":
			allowed = req.Status == "ready_to_resume" && req.CancelRequestedMS == 0
		}
		reason := ""
		if !allowed {
			reason = "operation is unavailable in the current execution state"
		}
		if permissionErr != nil {
			allowed = false
			reason = permissionErr.Error()
		}
		if allowed && action == "resume" {
			if _, _, _, err := s.manualAccess(ctx, p, req, true); err != nil {
				allowed = false
				reason = err.Error()
			}
		}
		out.AllowedActions = append(out.AllowedActions, model.ExecutionAllowedAction{Action: action, Allowed: allowed, Reason: reason})
	}
	if err = s.appendOperationDetail(ctx, &out, d); err != nil {
		return out, err
	}
	for _, op := range out.Operations {
		if op.Result != nil {
			if _, err = s.executionReadAccess(ctx, p, *op.Result); err != nil {
				return model.ExecutionDetail{}, err
			}
		}
	}
	sort.SliceStable(out.Timeline, func(i, j int) bool {
		if out.Timeline[i].AtMS != out.Timeline[j].AtMS {
			return out.Timeline[i].AtMS < out.Timeline[j].AtMS
		}
		return out.Timeline[i].ID < out.Timeline[j].ID
	})
	return out, nil
}

func expectedString(v int64) string { return strconv.FormatInt(v, 10) }

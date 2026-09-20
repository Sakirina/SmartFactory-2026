package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type DispatchResult struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}
type Dispatcher interface {
	Send(context.Context, model.Step, string, int64) (DispatchResult, error)
}
type Coordinator interface {
	Acquire(context.Context, string, string, time.Duration) (uint64, func(), error)
	Ready(context.Context, model.Definition) error
}

var ErrLeaseHeld = errors.New("another coordinator owns this execution")

type ExecutionCoordinator interface {
	Coordinator
	Validate(context.Context, string, string, uint64) error
	Checkpoint(context.Context, model.Execution) error
	Recover(context.Context, string) (model.Execution, error)
}
type executionKey struct{}

func ExecutionFromContext(ctx context.Context) (model.Execution, bool) {
	execution, ok := ctx.Value(executionKey{}).(model.Execution)
	return execution, ok
}

type Service struct {
	Store       *store.Store
	Identity    *identity.Manager
	Definitions *engine.Service
	Dispatcher  Dispatcher
	Coordinator Coordinator
	NodeID      string
	Edge        bool
}
type Check struct {
	Allowed     bool                `json:"allowed"`
	Interlocked bool                `json:"interlocked"`
	Reasons     []string            `json:"reasons"`
	Snapshot    []model.Observation `json:"snapshot"`
	Basis       []any               `json:"basis"`
}

func (s *Service) Conditions(ctx context.Context, d model.Definition) (Check, error) {
	result := Check{Allowed: true, Reasons: []string{}, Snapshot: []model.Observation{}, Basis: []any{}}
	for _, c := range d.Policy.Conditions {
		p, e := s.Store.Latest(ctx, c.DeviceID, c.Key)
		reason := ""
		passed := false
		if errors.Is(e, store.ErrNotFound) {
			reason = "missing observation"
		} else if e != nil {
			return result, e
		} else {
			maxAge := c.MaxAgeMS
			if maxAge <= 0 {
				maxAge = 5000
				doc, e := s.Store.Get(ctx, "entity", c.DeviceID)
				if e == nil {
					entity, e := store.Decode[model.Entity](doc)
					if e != nil {
						return result, e
					}
					if entity.SamplingMS*3 > maxAge {
						maxAge = entity.SamplingMS * 3
					}
				}
			}
			if p.Quality != "GOOD" {
				reason = "quality is " + p.Quality
			} else if s.Store.Now().UnixMilli()-p.ObservedMS > maxAge {
				reason = "source data is stale"
			} else {
				val, e := engine.Expression(ctx, "value "+c.Operator+" expected", map[string]any{"value": p.Value, "expected": c.Value})
				if e != nil {
					return result, e
				}
				passed, _ = val.(bool)
				if !passed {
					reason = "condition is not satisfied"
				}
			}
			result.Snapshot = append(result.Snapshot, p)
		}
		result.Basis = append(result.Basis, map[string]any{"condition": c, "passed": passed, "reason": reason})
		if !passed {
			result.Allowed = false
			result.Reasons = append(result.Reasons, c.DeviceID+"."+c.Key+": "+reason)
			if c.Interlock {
				result.Interlocked = true
			}
		}
	}
	return result, nil
}
func binding(d model.Definition, e model.Execution, check Check) string {
	return store.Hash([]any{d.ID, d.Version, d.Policy, e.Params, e.Override, e.RiskCategory, e.RiskLevel, check.Basis})
}
func (s *Service) Create(ctx context.Context, p identity.Principal, definitionID string, params map[string]string, override bool, id string) (model.Execution, error) {
	d, e := s.Definitions.Published(ctx, definitionID, 0)
	if e != nil {
		return model.Execution{}, e
	}
	if e = s.Identity.Permit(ctx, p, "control", d.GroupID); e != nil {
		return model.Execution{}, e
	}
	if d.Kind != "strategy" || d.Status != "published" {
		return model.Execution{}, errors.New("strategy is not published")
	}
	check, e := s.Conditions(ctx, d)
	if e != nil {
		return model.Execution{}, e
	}
	if id == "" {
		id = identity.ID()
	}
	req := model.Execution{DownlinkID: id, DefinitionID: d.ID, DefinitionVersion: d.Version, Params: params, Status: "awaiting_approval", Override: override, RiskCategory: d.Policy.RiskCategory, RiskLevel: d.Policy.RiskLevel, CreatedMS: s.Store.Now().UnixMilli(), ApprovalExpiresMS: s.Store.Now().Add(store.Milliseconds(s.Store.Policy().ApprovalTTLMS)).UnixMilli(), Approvals: []model.Approval{}, Steps: []model.StepResult{}, Snapshot: check.Snapshot, Actor: p.Actor, Version: 1}
	req.Binding = binding(d, req, check)
	if !check.Allowed && !override {
		req.Reason = strings.Join(check.Reasons, "; ")
	}
	if check.Interlocked {
		req.Status = "rejected"
		req.Reason = "physical interlock: " + strings.Join(check.Reasons, "; ")
	}
	e = s.Store.Write(ctx, func(t *store.Tx) error {
		if old, e := t.Get("execution", id); e == nil {
			previous, e := store.Decode[model.Execution](old)
			if e != nil {
				return e
			}
			if previous.DefinitionID != req.DefinitionID || previous.DefinitionVersion != req.DefinitionVersion || store.Hash(previous.Params) != store.Hash(req.Params) || previous.Override != req.Override {
				return store.ErrConflict
			}
			req = previous
			return nil
		}
		if _, e := t.Put("execution", id, 0, req); e != nil {
			return e
		}
		return t.Audit(p.Actor, "control.request", d.ID, id, req)
	})
	return req, e
}
func (s *Service) Get(ctx context.Context, id string) (model.Execution, error) {
	doc, e := s.Store.Get(ctx, "execution", id)
	if e != nil {
		return model.Execution{}, e
	}
	v, e := store.Decode[model.Execution](doc)
	v.Version = doc.Version
	return v, e
}
func (s *Service) Approve(ctx context.Context, p identity.Principal, id, role string) (model.Execution, error) {
	req, e := s.Get(ctx, id)
	if e != nil {
		return req, e
	}
	d, e := s.Definitions.Published(ctx, req.DefinitionID, 0)
	if e != nil {
		return req, e
	}
	if e = s.Identity.Permit(ctx, p, "approve", d.GroupID); e != nil {
		return req, e
	}
	if p.User.AI || !has(p.User.Roles, role) || !has([]string{"engineer", "leader", "safety"}, role) {
		return req, identity.ErrDenied
	}
	if role == "leader" {
		if e = s.leaderEligible(ctx, p.User, req); e != nil {
			return req, e
		}
	}
	if req.Status != "awaiting_approval" && req.Status != "approved" {
		return req, errors.New("execution does not accept approvals")
	}
	if s.Store.Now().UnixMilli() >= req.ApprovalExpiresMS {
		return req, errors.New("approval window expired")
	}
	if d.Version != req.DefinitionVersion {
		return req, store.ErrConflict
	}
	check, e := s.Conditions(ctx, d)
	if e != nil {
		return req, e
	}
	if check.Interlocked {
		return req, errors.New("physical interlock is active")
	}
	if req.Binding != binding(d, req, check) {
		return req, errors.New("risk basis changed; create a new approval request")
	}
	if role == "safety" {
		if !s.Edge || !p.Local || p.StepUpUntilMS <= s.Store.Now().UnixMilli() || d.Policy.SafetyUserID == "" || p.User.ID != d.Policy.SafetyUserID {
			return req, identity.ErrDenied
		}
	}
	for _, a := range req.Approvals {
		if a.UserID == p.User.ID {
			if a.Role == role {
				return req, nil
			}
			return req, errors.New("one person cannot fill multiple approval roles")
		}
	}
	req.Approvals = append(req.Approvals, model.Approval{UserID: p.User.ID, Role: role, AtMS: s.Store.Now().UnixMilli(), Binding: req.Binding, Local: p.Local, StepUp: p.StepUpUntilMS > s.Store.Now().UnixMilli(), Actor: p.Actor})
	if s.enough(req) {
		req.Status = "approved"
	}
	expected := req.Version
	req.Version++
	e = s.Store.Write(ctx, func(t *store.Tx) error {
		if _, e := t.Put("execution", id, expected, req); e != nil {
			return e
		}
		return t.Audit(p.Actor, "control.approve", d.ID, id, req)
	})
	return req, e
}
func has(values []string, v string) bool {
	for _, s := range values {
		if s == v {
			return true
		}
	}
	return false
}
func (s *Service) enough(req model.Execution) bool {
	policy := s.Store.Policy().Confirmations
	needEngineers, needLeaders := policy.Engineers, policy.Leaders
	if req.Override {
		needEngineers, needLeaders = policy.ForceEngineers, policy.ForceLeaders
	}
	engineers, leaders, safety := 0, 0, 0
	seen := map[string]bool{}
	for _, a := range req.Approvals {
		if seen[a.UserID] || a.Binding != req.Binding {
			continue
		}
		seen[a.UserID] = true
		switch a.Role {
		case "engineer":
			engineers++
		case "leader":
			leaders++
		case "safety":
			if a.Local && a.StepUp {
				safety++
			}
		}
	}
	return engineers >= needEngineers && leaders >= needLeaders && (!req.Override || req.RiskCategory != "safety" || safety >= 1)
}
func (s *Service) validateApprovals(ctx context.Context, req model.Execution) error {
	if s.Store.Now().UnixMilli() >= req.ApprovalExpiresMS {
		return errors.New("approval window expired")
	}
	if !s.enough(req) {
		return errors.New("required approvals are missing")
	}
	for _, approval := range req.Approvals {
		doc, e := s.Store.Get(ctx, "user", approval.UserID)
		if e != nil {
			return e
		}
		u, e := store.Decode[model.User](doc)
		if e != nil {
			return e
		}
		if !u.Active || !has(u.Roles, approval.Role) {
			return errors.New("approval signer is no longer authorized")
		}
		if approval.Role == "leader" {
			if e = s.leaderEligible(ctx, u, req); e != nil {
				return e
			}
		}
		p := identity.Principal{User: u}
		d, e := s.Definitions.Published(ctx, req.DefinitionID, 0)
		if e != nil {
			return e
		}
		if e = s.Identity.Permit(ctx, p, "approve", d.GroupID); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) Dispatch(ctx context.Context, p identity.Principal, id string) (model.Execution, error) {
	req, e := s.Get(ctx, id)
	if e != nil {
		return req, e
	}
	d, e := s.Definitions.Published(ctx, req.DefinitionID, 0)
	if e != nil {
		return req, e
	}
	if e = s.Identity.Permit(ctx, p, "control", d.GroupID); e != nil {
		return req, e
	}
	if req.Status != "approved" {
		return req, errors.New("execution is not approved")
	}
	if e = s.validateApprovals(ctx, req); e != nil {
		return req, e
	}
	if d.Version != req.DefinitionVersion || d.Status != "published" {
		return req, store.ErrConflict
	}
	if !s.Edge {
		node := firstEdge(d)
		doc, err := s.Store.Get(ctx, "entity", node)
		if err != nil {
			return req, fmt.Errorf("target node unavailable: %w", err)
		}
		entity, err := store.Decode[model.Entity](doc)
		if err != nil {
			return req, err
		}
		if entity.Kind != "edge" || entity.Status != "active" {
			return s.reject(ctx, req, Check{Reasons: []string{"target node is not admitted"}})
		}
		doc, err = s.Store.Get(ctx, "source", node)
		if err != nil {
			return s.reject(ctx, req, Check{Reasons: []string{"target node has no heartbeat"}})
		}
		source, err := store.Decode[model.SourceState](doc)
		if err != nil {
			return req, err
		}
		if s.Store.Now().UnixMilli()-source.LastSeenMS > s.Store.Policy().OfflineMS {
			return s.reject(ctx, req, Check{Reasons: []string{"target node is offline"}})
		}
	}
	check, e := s.Conditions(ctx, d)
	if e != nil {
		return req, e
	}
	if check.Interlocked || (!check.Allowed && !req.Override) {
		return s.reject(ctx, req, check)
	}
	if req.Binding != binding(d, req, check) {
		return req, errors.New("risk basis changed; approval must be renewed")
	}
	req.StartDeadlineMS = s.Store.Now().Add(store.Milliseconds(s.Store.Policy().StartTTLMS)).UnixMilli()
	req.Status = "queued"
	expected := req.Version
	req.Version++
	e = s.Store.Write(ctx, func(t *store.Tx) error {
		if _, e := t.Put("execution", id, expected, req); e != nil {
			return e
		}
		if e := t.Enqueue("downlink:"+id, "edge_downlink", firstEdge(d), req); e != nil {
			return e
		}
		return t.Audit(p.Actor, "control.dispatch", d.ID, id, req)
	})
	return req, e
}
func firstEdge(d model.Definition) string {
	if len(d.Policy.EdgeIDs) > 0 {
		return d.Policy.EdgeIDs[0]
	}
	if len(d.Policy.Steps) > 0 {
		return d.Policy.Steps[0].EdgeID
	}
	return ""
}
func (s *Service) reject(ctx context.Context, req model.Execution, check Check) (model.Execution, error) {
	req.Status = "rejected"
	req.Reason = strings.Join(check.Reasons, "; ")
	if check.Interlocked {
		req.Reason = "physical interlock: " + req.Reason
	}
	req.Snapshot = check.Snapshot
	expected := req.Version
	req.Version++
	e := s.Store.Write(ctx, func(t *store.Tx) error {
		if _, e := t.Put("execution", req.DownlinkID, expected, req); e != nil {
			return e
		}
		if s.Edge {
			if e := t.Enqueue(fmt.Sprintf("receipt:%s:%d", req.DownlinkID, req.Version), "cloud_receipt", s.NodeID, req); e != nil {
				return e
			}
		}
		return t.Audit(req.Actor, "control.reject", req.DefinitionID, req.DownlinkID, req)
	})
	return req, e
}
func (s *Service) Run(ctx context.Context, req model.Execution, automatic bool) (model.Execution, error) {
	if !s.Edge {
		return req, errors.New("device execution is only available on an edge")
	}
	if old, e := s.Get(ctx, req.DownlinkID); e == nil {
		if old.DefinitionID != req.DefinitionID || old.Binding != req.Binding {
			return req, store.ErrConflict
		}
		req = old
		if terminal(req.Status) {
			return req, nil
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return req, e
	} else {
		req.Version = 1
		if _, e = s.Store.Put(ctx, "execution", req.DownlinkID, 0, req); e != nil {
			return req, e
		}
	}
	currentDefinition, definitionError := s.Definitions.Published(ctx, req.DefinitionID, 0)
	if definitionError != nil {
		return s.reject(ctx, req, Check{Reasons: []string{"published policy unavailable: " + definitionError.Error()}})
	}
	var coordinationFailure error
	if journal, ok := s.Coordinator.(ExecutionCoordinator); ok && (len(currentDefinition.Policy.EdgeIDs) > 1 || req.Fence > 0) {
		if recovered, err := journal.Recover(ctx, req.DownlinkID); err == nil {
			if recovered.DefinitionID != req.DefinitionID || recovered.Binding != req.Binding {
				return req, store.ErrConflict
			}
			localVersion := req.Version
			req = recovered
			req.Version = localVersion
			if terminal(req.Status) {
				err = s.save(ctx, &req, "control.reconciled")
				return req, err
			}
		} else if req.Status == "running" && !errors.Is(err, store.ErrNotFound) {
			coordinationFailure = fmt.Errorf("cannot reconcile the in-progress execution: %w", err)
		}
	}
	var d model.Definition
	var e error
	if req.Status == "running" {
		d, e = s.Definitions.Version(ctx, req.DefinitionID, req.DefinitionVersion)
	} else {
		d = currentDefinition
	}
	if e != nil {
		return s.reject(ctx, req, Check{Reasons: []string{"published policy unavailable: " + e.Error()}})
	}
	if d.Status != "published" || d.Version != req.DefinitionVersion {
		return s.reject(ctx, req, Check{Reasons: []string{"published policy version changed"}})
	}
	if req.Status != "running" && s.Store.Now().UnixMilli() >= req.StartDeadlineMS {
		return s.reject(ctx, req, Check{Reasons: []string{"execution start deadline expired"}})
	}
	if !automatic && req.Status != "running" {
		if e = s.validateApprovals(ctx, req); e != nil {
			return s.reject(ctx, req, Check{Reasons: []string{e.Error()}})
		}
	}
	check, e := s.Conditions(ctx, d)
	if e != nil {
		return req, e
	}
	steps := d.Policy.Steps
	release := func() {}
	degraded := false
	if len(d.Policy.EdgeIDs) > 1 {
		if coordinationFailure != nil {
			e = coordinationFailure
		} else if s.Coordinator == nil {
			e = errors.New("coordination service is unavailable")
		} else if e = s.Coordinator.Ready(ctx, d); e == nil {
			req.Fence, release, e = s.Coordinator.Acquire(ctx, req.DownlinkID, s.NodeID, time.Minute)
			if e == nil {
				req.CoordinatorID = s.NodeID
			}
		}
		if e != nil {
			if errors.Is(e, ErrLeaseHeld) {
				return req, e
			}
			coordinationError := e
			steps = d.Policy.Degraded
			degraded = true
			req.Fence = 0
			req.CoordinatorID = ""
			localDefinition := d
			localDefinition.Policy.Conditions = nil
			for _, condition := range d.Policy.Conditions {
				doc, err := s.Store.Get(ctx, "entity", condition.DeviceID)
				if err != nil {
					return req, err
				}
				entity, err := store.Decode[model.Entity](doc)
				if err != nil {
					return req, err
				}
				if entity.EdgeID == s.NodeID {
					localDefinition.Policy.Conditions = append(localDefinition.Policy.Conditions, condition)
				}
			}
			check, e = s.Conditions(ctx, localDefinition)
			if e != nil {
				return req, e
			}
			req.Reason = "configured degraded branch: " + coordinationError.Error()
			if len(steps) == 0 {
				return s.reject(ctx, req, Check{Reasons: []string{req.Reason}})
			}
		}
	}
	if release != nil {
		defer release()
	}
	if check.Interlocked || (!check.Allowed && !req.Override) {
		return s.reject(ctx, req, check)
	}
	req.Status = "running"
	req.Snapshot = check.Snapshot
	if e = s.save(ctx, &req, "control.start"); e != nil {
		return req, e
	}
	for _, step := range steps {
		completed := false
		for _, result := range req.Steps {
			if result.StepID == step.ID && result.Status == "SUCCESS" {
				completed = true
				break
			}
		}
		if completed {
			continue
		}
		if step.EdgeID != s.NodeID && degraded {
			continue
		}
		if s.Dispatcher == nil {
			return req, errors.New("device dispatcher is not configured")
		}
		step.Params = bindParams(step.Params, req.Params)
		commandID := req.DownlinkID + ":" + step.ID
		result, e := s.runStep(ctx, req, step, commandID)
		if e != nil {
			return req, e
		}
		req.Steps = replaceStep(req.Steps, result)
		if e = s.save(ctx, &req, "control.step"); e != nil {
			return req, e
		}
		if result.Status != "SUCCESS" {
			if result.Status == "RESULT_UNKNOWN" {
				req.Status = "result_unknown"
			} else {
				req.Status = "failed"
			}
			req.Reason = result.Message
			break
		}
	}
	if req.Status == "running" {
		req.Status = "completed"
		if degraded {
			req.Status = "degraded_completed"
		}
	}
	e = s.save(ctx, &req, "control.finish")
	return req, e
}
func terminal(status string) bool {
	return has([]string{"completed", "degraded_completed", "failed", "rejected", "result_unknown"}, status)
}
func bindParams(original, params map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range original {
		for name, value := range params {
			v = strings.ReplaceAll(v, "${"+name+"}", value)
		}
		out[k] = v
	}
	return out
}
func replaceStep(results []model.StepResult, r model.StepResult) []model.StepResult {
	for i, v := range results {
		if v.StepID == r.StepID {
			results[i] = r
			return results
		}
	}
	return append(results, r)
}
func (s *Service) save(ctx context.Context, req *model.Execution, action string) error {
	expected := req.Version
	req.Version++
	err := s.Store.Write(ctx, func(t *store.Tx) error {
		if _, e := t.Put("execution", req.DownlinkID, expected, req); e != nil {
			return e
		}
		if e := t.Enqueue(fmt.Sprintf("receipt:%s:%d", req.DownlinkID, req.Version), "cloud_receipt", s.NodeID, req); e != nil {
			return e
		}
		return t.Audit(req.Actor, action, req.DefinitionID, req.DownlinkID, req)
	})
	if err == nil && req.Fence > 0 && req.CoordinatorID == s.NodeID {
		if journal, ok := s.Coordinator.(ExecutionCoordinator); ok {
			return journal.Checkpoint(ctx, *req)
		}
	}
	return err
}
func (s *Service) runStep(ctx context.Context, req model.Execution, step model.Step, id string) (model.StepResult, error) {
	r := model.StepResult{StepID: step.ID, CommandID: id, StartedMS: s.Store.Now().UnixMilli()}
	if req.Fence > 0 {
		if coordinator, ok := s.Coordinator.(ExecutionCoordinator); ok {
			if e := coordinator.Validate(ctx, req.DownlinkID, req.CoordinatorID, req.Fence); e != nil {
				return r, e
			}
		} else {
			return r, errors.New("execution fencing is unavailable")
		}
	}
	hash := store.Hash(step)
	execute := false
	e := s.Store.Write(ctx, func(t *store.Tx) error {
		var oldHash, status, b string
		err := t.QueryRowContext(ctx, "SELECT payload_hash,status,data FROM action_journal WHERE command_id=$1", id).Scan(&oldHash, &status, &b)
		if err == nil {
			if oldHash != hash {
				return store.ErrConflict
			}
			if err = store.DecodeJSON([]byte(b), &r); err != nil {
				return err
			}
			if status == "reserved" {
				r.Status = "RESULT_UNKNOWN"
				r.Message = "previous process stopped after reserving the action; reconcile device state"
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		r.Status = "RESULT_UNKNOWN"
		r.Message = "action reserved"
		raw, _ := json.Marshal(r)
		_, err = t.ExecContext(ctx, "INSERT INTO action_journal(command_id,payload_hash,status,fence,data,updated_ms) VALUES($1,$2,'reserved',$3,$4,$5)", id, hash, req.Fence, string(raw), s.Store.Now().UnixMilli())
		execute = err == nil
		return err
	})
	if e != nil || !execute {
		return r, e
	}
	timeout := step.TimeoutMS
	if timeout <= 0 {
		timeout = 5000
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	runCtx = context.WithValue(runCtx, executionKey{}, req)
	// The reservation survives a crash. Recheck interlocks and fencing after
	// committing it, immediately before the physical write.
	result, err := s.beforeStep(runCtx, req, step)
	if err == nil && result.Status == "" {
		result, err = s.Dispatcher.Send(runCtx, step, id, s.Store.Now().Add(time.Duration(timeout)*time.Millisecond).UnixMilli())
	}
	r.FinishedMS = s.Store.Now().UnixMilli()
	r.Status = result.Status
	r.Message = result.Message
	if err != nil {
		r.Status = "RESULT_UNKNOWN"
		r.Message = err.Error()
	}
	if r.Status == "" {
		r.Status = "RESULT_UNKNOWN"
		r.Message = "device did not return a business result"
	}
	e = s.Store.Write(ctx, func(t *store.Tx) error {
		raw, _ := json.Marshal(r)
		_, e := t.ExecContext(ctx, "UPDATE action_journal SET status=$1,data=$2,updated_ms=$3 WHERE command_id=$4", r.Status, string(raw), s.Store.Now().UnixMilli(), id)
		return e
	})
	return r, e
}

func (s *Service) beforeStep(ctx context.Context, req model.Execution, step model.Step) (DispatchResult, error) {
	if step.EdgeID == s.NodeID {
		d, e := s.Definitions.Version(ctx, req.DefinitionID, req.DefinitionVersion)
		if e != nil {
			return DispatchResult{Status: "REJECTED", Message: e.Error()}, nil
		}
		local := d
		local.Policy.Conditions = nil
		for _, condition := range d.Policy.Conditions {
			owned := condition.DeviceID == step.DeviceID
			if doc, err := s.Store.Get(ctx, "entity", condition.DeviceID); err == nil {
				entity, err := store.Decode[model.Entity](doc)
				if err != nil {
					return DispatchResult{}, err
				}
				owned = entity.EdgeID == s.NodeID
			} else if !errors.Is(err, store.ErrNotFound) {
				return DispatchResult{}, err
			}
			if owned {
				local.Policy.Conditions = append(local.Policy.Conditions, condition)
			}
		}
		check, e := s.Conditions(ctx, local)
		if e != nil {
			return DispatchResult{}, e
		}
		if check.Interlocked || !check.Allowed && !req.Override {
			return DispatchResult{Status: "REJECTED", Message: strings.Join(check.Reasons, "; ")}, nil
		}
	}
	if req.Fence > 0 {
		coordinator, ok := s.Coordinator.(ExecutionCoordinator)
		if !ok {
			return DispatchResult{Status: "REJECTED", Message: "execution fencing is unavailable"}, nil
		}
		if e := coordinator.Validate(ctx, req.DownlinkID, req.CoordinatorID, req.Fence); e != nil {
			return DispatchResult{Status: "REJECTED", Message: e.Error()}, nil
		}
	}
	return DispatchResult{}, nil
}

package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/observability"
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
type CommandFeedback struct {
	CommandID   string `json:"command_id"`
	DeviceID    string `json:"device_id"`
	PayloadHash string `json:"payload_hash"`
	RequestHash string `json:"request_hash,omitempty"`
	Source      string `json:"source"`
	ObservedMS  int64  `json:"observed_ms"`
	Found       bool   `json:"found"`
	Bound       bool   `json:"bound"`
	Status      string `json:"status"`
	Message     string `json:"message,omitempty"`
	ContentHash string `json:"content_hash"`
}
type ResultQuerier interface {
	LookupCommand(context.Context, model.Step, string) (CommandFeedback, error)
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
	Store       Repository
	Identity    *identity.Manager
	Definitions *engine.Service
	Dispatcher  Dispatcher
	Coordinator Coordinator
	NodeID      string
	Edge        bool
	// Fault is an optional test seam at committed action phases. Production
	// construction leaves it nil; returned errors interrupt the physical pipeline.
	Fault func(context.Context, string, model.Execution, string) error
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
			} else if s.Store.CurrentTime().UnixMilli()-p.ObservedMS > maxAge {
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
func (s *Service) Create(ctx context.Context, p identity.Principal, definitionID string, params map[string]string, override bool, id string) (_ model.Execution, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "control.request", observability.Identity{ActorID: p.User.ID, RequestID: id, DefinitionID: definitionID})
	defer func() { finish(operationErr) }()
	d, e := s.Definitions.Published(ctx, definitionID, 0)
	if e != nil {
		return model.Execution{}, e
	}
	p, access, e := s.authorize(ctx, p, d, "control")
	if e != nil {
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
	req := model.Execution{DownlinkID: id, DefinitionID: d.ID, DefinitionVersion: d.Version, Params: params, Status: "awaiting_approval", Override: override, RiskCategory: d.Policy.RiskCategory, RiskLevel: d.Policy.RiskLevel, CreatedMS: s.Store.CurrentTime().UnixMilli(), ApprovalExpiresMS: s.Store.CurrentTime().Add(store.Milliseconds(s.Store.Policy().ApprovalTTLMS)).UnixMilli(), Approvals: []model.Approval{}, Steps: []model.StepResult{}, Snapshot: check.Snapshot, Actor: p.Actor, Version: 1}
	req.Mode = "manual"
	req.ResourceBinding = access.binding()
	req.Binding = binding(d, req, check)
	if !check.Allowed && !override {
		req.Reason = strings.Join(check.Reasons, "; ")
	}
	if check.Interlocked {
		req.Status = "rejected"
		req.Reason = "physical interlock: " + strings.Join(check.Reasons, "; ")
	}
	e = s.Store.Write(ctx, func(t *store.Tx) error {
		if err := t.CheckRevisions(access.revisions); err != nil {
			return err
		}
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
		return ApplyTransition(t, &req, 0, "request", s.transitionOptions(req))
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
	return s.ApproveVersion(ctx, p, id, role, nil)
}
func (s *Service) ApproveVersion(ctx context.Context, p identity.Principal, id, role string, expectedVersion *int64) (_ model.Execution, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "control.approve", observability.Identity{ActorID: p.User.ID, RequestID: id})
	defer func() { finish(operationErr) }()
	req, e := s.Get(ctx, id)
	if e != nil {
		return req, e
	}
	d, e := s.Definitions.Published(ctx, req.DefinitionID, 0)
	if e != nil {
		return req, e
	}
	p, access, e := s.authorize(ctx, p, d, "approve")
	if e != nil {
		return req, e
	}
	if expectedVersion != nil && req.Version != *expectedVersion {
		return req, store.ErrConflict
	}
	if e = verifyResourceBinding(req, access); e != nil {
		return req, e
	}
	if e = s.organizationSnapshot(ctx, req, access); e != nil {
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
	if s.Store.CurrentTime().UnixMilli() >= req.ApprovalExpiresMS {
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
		if !s.Edge || !p.Local || p.StepUpUntilMS <= s.Store.CurrentTime().UnixMilli() || d.Policy.SafetyUserID == "" || p.User.ID != d.Policy.SafetyUserID {
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
	req.Approvals = append(req.Approvals, model.Approval{UserID: p.User.ID, Role: role, AtMS: s.Store.CurrentTime().UnixMilli(), Binding: req.Binding, Local: p.Local, StepUp: p.StepUpUntilMS > s.Store.CurrentTime().UnixMilli(), Actor: p.Actor})
	if s.enough(req) {
		req.Status = "approved"
	}
	expected := req.Version
	e = s.Store.Write(ctx, func(t *store.Tx) error {
		if err := t.CheckRevisions(access.revisions); err != nil {
			return err
		}
		opts := s.transitionOptions(req)
		opts.Actor = p.Actor
		return ApplyTransition(t, &req, expected, "approve", opts)
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
	if s.Store.CurrentTime().UnixMilli() >= req.ApprovalExpiresMS {
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
	return s.DispatchVersion(ctx, p, id, nil)
}
func (s *Service) DispatchVersion(ctx context.Context, p identity.Principal, id string, expectedVersion *int64) (_ model.Execution, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "control.dispatch", observability.Identity{ActorID: p.User.ID, RequestID: id})
	defer func() { finish(operationErr) }()
	req, e := s.Get(ctx, id)
	if e != nil {
		return req, e
	}
	d, e := s.Definitions.Published(ctx, req.DefinitionID, 0)
	if e != nil {
		return req, e
	}
	p, access, e := s.authorize(ctx, p, d, "control")
	if e != nil {
		return req, e
	}
	if expectedVersion != nil && req.Version != *expectedVersion {
		return req, store.ErrConflict
	}
	if e = verifyResourceBinding(req, access); e != nil {
		return req, e
	}
	executionAccess, e := s.executionAccess(ctx, req, d)
	if e != nil {
		return req, e
	}
	access.revisions = append(access.revisions, executionAccess.revisions...)
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
		if s.Store.CurrentTime().UnixMilli()-source.LastSeenMS > s.Store.Policy().OfflineMS {
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
	req.StartDeadlineMS = s.Store.CurrentTime().Add(store.Milliseconds(s.Store.Policy().StartTTLMS)).UnixMilli()
	req.Status = "queued"
	expected := req.Version
	e = s.Store.Write(ctx, func(t *store.Tx) error {
		if err := t.CheckRevisions(access.revisions); err != nil {
			return err
		}
		opts := s.transitionOptions(req)
		opts.Actor = p.Actor
		if err := ApplyTransition(t, &req, expected, "dispatch", opts); err != nil {
			return err
		}
		if e := t.Enqueue("downlink:"+id, "edge_downlink", firstEdge(d), req); e != nil {
			return e
		}
		return nil
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

package control

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type accessSnapshot struct {
	revisions []store.Revision
	resources map[string]int64
	seen      map[string]int64
}

func newAccess() *accessSnapshot {
	return &accessSnapshot{resources: map[string]int64{}, seen: map[string]int64{}}
}
func (a *accessSnapshot) add(kind, id string, version int64) error {
	key := kind + ":" + id
	if previous, ok := a.seen[key]; ok {
		if previous != version {
			return store.ErrConflict
		}
		return nil
	}
	a.seen[key] = version
	a.revisions = append(a.revisions, store.Revision{Kind: kind, ID: id, Version: version})
	if kind == "entity" || kind == "definition" {
		a.resources[key] = version
	}
	return nil
}
func (a *accessSnapshot) binding() string { return store.Hash(a.resources) }

func (s *Service) currentPrincipal(ctx context.Context, p identity.Principal, access *accessSnapshot) (identity.Principal, error) {
	if p.User.ID == "" || s.Identity == nil {
		return p, identity.ErrDenied
	}
	doc, err := s.Store.Get(ctx, "user", p.User.ID)
	if err != nil {
		return p, identity.ErrDenied
	}
	u, err := store.Decode[model.User](doc)
	if err != nil {
		return p, err
	}
	if !u.Active {
		return p, identity.ErrDenied
	}
	if p.User.Version != 0 && p.User.Version != u.Version {
		return p, store.ErrConflict
	}
	if p.User.AI {
		u.AI = true
		u.Roles = p.User.Roles
	}
	p.User = u
	sessionID := p.SessionID
	if sessionID == "" {
		sessionID = p.Actor.SessionID
	}
	p.Actor = model.Actor{UserID: u.ID, Name: u.Name, DepartmentID: u.DepartmentID, Roles: u.Roles, Source: p.Actor.Source, SessionID: sessionID, AI: u.AI}
	if err = access.add("user", u.ID, doc.Version); err != nil {
		return p, err
	}
	if p.SessionDocument != "" {
		d, err := s.Store.Get(ctx, "session", p.SessionDocument)
		if err != nil {
			return p, identity.ErrAuthentication
		}
		session, err := store.Decode[identity.Session](d)
		if err != nil || d.Version != p.SessionVersion || session.UserID != u.ID || session.ExpiresMS <= s.Store.CurrentTime().UnixMilli() {
			return p, identity.ErrAuthentication
		}
		if err = access.add("session", d.ID, d.Version); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (s *Service) captureGrants(ctx context.Context, access *accessSnapshot) error {
	docs, err := s.Store.List(ctx, "grant")
	if err != nil {
		return err
	}
	members := map[string]int64{}
	for _, d := range docs {
		members[d.ID] = d.Version
	}
	access.revisions = append(access.revisions, store.Revision{Kind: "grant", Members: members})
	return nil
}

func (s *Service) permitResource(ctx context.Context, p identity.Principal, action, resource string, access *accessSnapshot) error {
	if resource == "" {
		return identity.ErrDenied
	}
	visited := map[string]bool{}
	for id, depth := resource, 0; id != "" && depth < 64; depth++ {
		if visited[id] {
			return errors.New("asset ancestry contains a cycle")
		}
		visited[id] = true
		doc, err := s.Store.Get(ctx, "entity", id)
		if errors.Is(err, store.ErrNotFound) {
			return errors.Join(access.add("entity", id, 0), s.Identity.Permit(ctx, p, action, resource))
		}
		if err != nil {
			return err
		}
		if err = access.add("entity", id, doc.Version); err != nil {
			return err
		}
		entity, err := store.Decode[model.Entity](doc)
		if err != nil {
			return err
		}
		id = entity.ParentID
	}
	return s.Identity.Permit(ctx, p, action, resource)
}

func (s *Service) definitionAccess(ctx context.Context, p identity.Principal, d model.Definition, action string, access *accessSnapshot) error {
	seen := map[string]bool{}
	visiting := map[string]bool{}
	var visit func(model.Definition, string) error
	visit = func(def model.Definition, permission string) error {
		if visiting[def.ID] {
			return errors.New("policy dependency contains a cycle")
		}
		if seen[def.ID] {
			return nil
		}
		doc, err := s.Store.Get(ctx, "definition", def.ID)
		if err != nil {
			return err
		}
		current, err := store.Decode[model.Definition](doc)
		if err != nil {
			return err
		}
		if current.Version != def.Version || current.Status != "published" {
			return store.ErrConflict
		}
		if err = access.add("definition", def.ID, doc.Version); err != nil {
			return err
		}
		if err = s.permitResource(ctx, p, permission, def.GroupID, access); err != nil {
			return err
		}
		resources := map[string]string{}
		for _, id := range def.Selector.DeviceIDs {
			resources[id] = "read"
		}
		if def.Selector.AssetID != "" {
			resources[def.Selector.AssetID] = "read"
		}
		for _, c := range def.Policy.Conditions {
			resources[c.DeviceID] = "read"
		}
		for _, steps := range [][]model.Step{def.Policy.Steps, def.Policy.Degraded} {
			for _, step := range steps {
				resources[step.DeviceID] = permission
			}
		}
		ids := make([]string, 0, len(resources))
		for id := range resources {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if err = s.permitResource(ctx, p, resources[id], id, access); err != nil {
				return err
			}
		}
		visiting[def.ID] = true
		for _, id := range def.Dependencies {
			other, err := s.Definitions.Published(ctx, id, 0)
			if err != nil {
				return err
			}
			if err = visit(other, "read"); err != nil {
				return err
			}
		}
		delete(visiting, def.ID)
		seen[def.ID] = true
		return nil
	}
	return visit(d, action)
}

func (s *Service) authorize(ctx context.Context, p identity.Principal, d model.Definition, action string) (identity.Principal, *accessSnapshot, error) {
	access := newAccess()
	p, err := s.currentPrincipal(ctx, p, access)
	if err != nil {
		return p, access, err
	}
	if action != "read" && p.User.AI {
		return p, access, identity.ErrDenied
	}
	if err = s.captureGrants(ctx, access); err != nil {
		return p, access, err
	}
	err = s.definitionAccess(ctx, p, d, action, access)
	return p, access, err
}

func (s *Service) organizationSnapshot(ctx context.Context, req model.Execution, access *accessSnapshot) error {
	people := []string{req.Actor.UserID}
	for _, approval := range req.Approvals {
		people = append(people, approval.UserID)
	}
	seen := map[string]bool{}
	for _, id := range people {
		for depth := 0; id != "" && depth < 64 && !seen[id]; depth++ {
			seen[id] = true
			doc, err := s.Store.Get(ctx, "user", id)
			if err != nil {
				return err
			}
			if err = access.add("user", id, doc.Version); err != nil {
				return err
			}
			u, err := store.Decode[model.User](doc)
			if err != nil {
				return err
			}
			for department, depth := u.DepartmentID, 0; department != "" && depth < 64; depth++ {
				d, err := s.Store.Get(ctx, "department", department)
				if errors.Is(err, store.ErrNotFound) {
					if err = access.add("department", department, 0); err != nil {
						return err
					}
					break
				}
				if err != nil {
					return err
				}
				if err = access.add("department", department, d.Version); err != nil {
					return err
				}
				var value struct {
					ParentID string `json:"parent_id"`
				}
				if err = store.DecodeJSON(d.Data, &value); err != nil {
					return err
				}
				department = value.ParentID
			}
			id = u.ManagerID
		}
	}
	return nil
}

func (s *Service) executionAccess(ctx context.Context, req model.Execution, d model.Definition) (*accessSnapshot, error) {
	if d.Kind != "strategy" {
		return nil, errors.New("control execution requires a published strategy")
	}
	if req.Mode == "automatic" || req.Mode == "scheduled" || req.Actor.UserID == "published-policy" {
		if req.Actor.UserID != "published-policy" || req.Override {
			return nil, identity.ErrDenied
		}
		// The service identity is restricted to this published policy's declared
		// assets. Entity/definition revisions are still pinned before every action.
		p := identity.Principal{User: model.User{ID: "published-policy", Active: true, Roles: []string{"engineer"}, Resources: []string{"*"}}}
		access := newAccess()
		if err := s.definitionAccess(ctx, p, d, "control", access); err != nil {
			return nil, err
		}
		return access, nil
	}
	p := identity.Principal{User: model.User{ID: req.Actor.UserID}, Actor: req.Actor}
	_, access, err := s.authorize(ctx, p, d, "control")
	if err != nil {
		return access, err
	}
	if err = s.organizationSnapshot(ctx, req, access); err != nil {
		return access, err
	}
	for _, approval := range req.Approvals {
		person := identity.Principal{User: model.User{ID: approval.UserID}, Actor: approval.Actor}
		person, err = s.currentPrincipal(ctx, person, access)
		if err != nil {
			return access, err
		}
		if !has(person.User.Roles, approval.Role) {
			return access, identity.ErrDenied
		}
		if err = s.definitionAccess(ctx, person, d, "approve", access); err != nil {
			return access, err
		}
		if approval.Role == "leader" {
			if err = s.leaderEligible(ctx, person.User, req); err != nil {
				return access, err
			}
		}
	}
	if !s.enough(req) {
		return access, errors.New("required approvals are missing")
	}
	return access, nil
}

func verifyResourceBinding(req model.Execution, access *accessSnapshot) error {
	if req.ResourceBinding != "" && req.ResourceBinding != access.binding() {
		return fmt.Errorf("%w: referenced policy or asset version changed", store.ErrConflict)
	}
	return nil
}

// executionReadAccess checks the resources actually returned by historical
// execution views against the reader's current account and asset ancestry.
func (s *Service) executionReadAccess(ctx context.Context, p identity.Principal, req model.Execution) (model.Definition, error) {
	access := newAccess()
	p, err := s.currentPrincipal(ctx, p, access)
	if err != nil {
		return model.Definition{}, err
	}
	d, err := s.Definitions.Version(ctx, req.DefinitionID, req.DefinitionVersion)
	if err != nil {
		return d, err
	}
	resources := map[string]bool{d.GroupID: true}
	if current, e := s.Store.Get(ctx, "definition", req.DefinitionID); e == nil {
		latest, e := store.Decode[model.Definition](current)
		if e != nil {
			return d, e
		}
		resources[latest.GroupID] = true
	} else if !errors.Is(e, store.ErrNotFound) {
		return d, e
	}
	for _, id := range d.Selector.DeviceIDs {
		resources[id] = true
	}
	if d.Selector.AssetID != "" {
		resources[d.Selector.AssetID] = true
	}
	for _, condition := range d.Policy.Conditions {
		resources[condition.DeviceID] = true
	}
	for _, steps := range [][]model.Step{d.Policy.Steps, d.Policy.Degraded} {
		for _, step := range steps {
			resources[step.DeviceID] = true
		}
	}
	for _, sample := range req.Snapshot {
		resources[sample.DeviceID] = true
	}
	evidence, err := s.Store.ExecutionEvidence(ctx, req.DownlinkID)
	if err != nil {
		return d, err
	}
	for _, item := range evidence {
		resources[item.DeviceID] = true
	}
	for resource := range resources {
		if err = s.permitResource(ctx, p, "read", resource, access); err != nil {
			return d, err
		}
	}
	return d, nil
}

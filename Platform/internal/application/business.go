package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type BusinessRepository interface {
	DefinitionRepository
	Versions(context.Context, string, string) ([]store.Document, error)
	AlarmCase(context.Context, model.Alarm) (model.AlarmCase, error)
	ExecutionEvidence(context.Context, string) ([]model.CommandEvidence, error)
}

type Business struct {
	Store       BusinessRepository
	Identity    *identity.Manager
	Definitions *Definitions
	NodeID      string
	Mode        string
}

func (s *Business) access() *authorizationReader {
	return &authorizationReader{Store: s.Store, Identity: s.Identity}
}

func (s *Business) authorize(ctx context.Context, p identity.Principal, action string, resources []string) (*revisions, error) {
	seen := newRevisions()
	doc, err := s.Store.Get(ctx, "user", p.User.ID)
	if err != nil {
		return nil, identity.ErrAuthentication
	}
	u, err := store.Decode[model.User](doc)
	if err != nil {
		return nil, err
	}
	if !u.Active {
		return nil, identity.ErrDenied
	}
	canonical := u
	if p.SessionDocument != "" {
		sessionDoc, e := s.Store.Get(ctx, "session", p.SessionDocument)
		if e != nil {
			return nil, identity.ErrAuthentication
		}
		session, e := store.Decode[identity.Session](sessionDoc)
		if e != nil || session.UserID != u.ID || session.ExpiresMS <= s.Store.CurrentTime().UnixMilli() || sessionDoc.Version != p.SessionVersion {
			return nil, identity.ErrAuthentication
		}
		if e = seen.remember("session", sessionDoc.ID, sessionDoc.Version); e != nil {
			return nil, e
		}
		if session.DelegatedAI {
			canonical.AI = true
			canonical.Roles = []string{"ai"}
			if session.AIReadOnly {
				canonical.Roles = []string{"viewer"}
			}
		}
	}
	if store.Hash(canonical) != store.Hash(p.User) || p.Actor.UserID != p.User.ID || p.Actor.AI != p.User.AI || p.Actor.DepartmentID != p.User.DepartmentID {
		return nil, identity.ErrDenied
	}
	// A signed permission bundle retains the user's source version while the
	// edge document has its own storage revision. Guard the actual document.
	if err = seen.remember("user", doc.ID, doc.Version); err != nil {
		return nil, err
	}
	grants, err := s.Store.List(ctx, "grant")
	if err != nil {
		return nil, err
	}
	seen.grants = map[string]int64{}
	for _, grant := range grants {
		seen.grants[grant.ID] = grant.Version
		if err = seen.remember("grant", grant.ID, grant.Version); err != nil {
			return nil, err
		}
	}
	if err = s.access().permit(ctx, p, action, "", seen); err != nil {
		return nil, err
	}
	for _, resource := range uniqueHistory(resources) {
		if resource == "" {
			return nil, identity.ErrDenied
		}
		if err = s.access().permit(ctx, p, action, resource, seen); err != nil {
			return nil, err
		}
	}
	return seen, nil
}

func (s *Business) read(ctx context.Context, kind, id string, seen *revisions) (store.Document, error) {
	doc, err := s.Store.Get(ctx, kind, id)
	if err != nil {
		return doc, err
	}
	return doc, seen.remember(kind, id, doc.Version)
}

func (s *Business) member(ctx context.Context, id string, resources []string, seen *revisions) (model.User, error) {
	doc, err := s.read(ctx, "user", id, seen)
	if err != nil {
		return model.User{}, fmt.Errorf("assignee unavailable: %w", err)
	}
	u, err := store.Decode[model.User](doc)
	if err != nil {
		return u, err
	}
	if !u.Active || u.AI {
		return u, errors.New("assignee must be an active human member")
	}
	if u.DepartmentID == "" {
		return u, errors.New("assignee requires a current department")
	}
	if _, err = s.read(ctx, "department", u.DepartmentID, seen); err != nil {
		return u, errors.New("assignee department is unavailable")
	}
	p := identity.Principal{User: u}
	for _, resource := range uniqueHistory(resources) {
		if err = s.access().permit(ctx, p, "approve", resource, seen); err != nil {
			return u, fmt.Errorf("assignee cannot handle associated resource: %w", err)
		}
	}
	return u, nil
}

func businessReason(reason string) error {
	if strings.TrimSpace(reason) == "" || len(reason) > 4000 {
		return errors.New("reason must contain 1 to 4000 characters")
	}
	return nil
}
func businessID(id string) error {
	if strings.TrimSpace(id) == "" || len(id) > 200 {
		return errors.New("id must contain 1 to 200 characters")
	}
	return nil
}

func (s *Business) alarm(ctx context.Context, p identity.Principal, id, action string) (model.Alarm, *revisions, error) {
	seen, err := s.authorize(ctx, p, action, nil)
	if err != nil {
		return model.Alarm{}, nil, err
	}
	doc, err := s.read(ctx, "alarm", id, seen)
	if err != nil {
		return model.Alarm{}, nil, err
	}
	alarm, err := store.Decode[model.Alarm](doc)
	if err != nil {
		return alarm, nil, err
	}
	if alarm.ID != id {
		return alarm, nil, store.ErrConflict
	}
	if err = s.access().permit(ctx, p, action, alarm.EntityID, seen); err != nil {
		return alarm, nil, err
	}
	def, err := s.read(ctx, "definition", alarm.DefinitionID, seen)
	if err != nil {
		return alarm, nil, err
	}
	d, err := store.Decode[model.Definition](def)
	if err != nil {
		return alarm, nil, err
	}
	if err = s.access().permit(ctx, p, "read", d.GroupID, seen); err != nil {
		return alarm, nil, err
	}
	return alarm, seen, nil
}

func (s *Business) AlarmDetail(ctx context.Context, p identity.Principal, id string) (model.AlarmDetail, error) {
	alarm, seen, err := s.alarm(ctx, p, id, "read")
	if err != nil {
		return model.AlarmDetail{}, err
	}
	c, err := s.Store.AlarmCase(ctx, alarm)
	if err != nil {
		return model.AlarmDetail{}, err
	}
	if err = seen.remember("alarm_case", id, c.Version); err != nil {
		return model.AlarmDetail{}, err
	}
	delete(seen.documents, documentKey{"alarm", id})
	detail := model.AlarmDetail{Alarm: alarm, ActionVersion: AlarmActionVersion(alarm), Case: c, AllowedActions: []model.BusinessAction{}}
	if err = s.nativeAlarmDetail(ctx, id, &detail); err != nil {
		return detail, err
	}
	for _, action := range []string{"acknowledge", "assign", "note", "complete", "reopen"} {
		e := s.alarmAction(ctx, p, alarm, c, action)
		allowed := model.BusinessAction{Action: action, Allowed: e == nil}
		if e != nil {
			allowed.Reason = e.Error()
		}
		detail.AllowedActions = append(detail.AllowedActions, allowed)
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
	return detail, err
}

func (s *Business) alarmAction(ctx context.Context, p identity.Principal, a model.Alarm, c model.AlarmCase, action string) error {
	if err := s.Identity.Permit(ctx, p, "approve", a.EntityID); err != nil {
		return err
	}
	if len(c.PendingBasis) > 0 {
		return errors.New("synchronization must obtain the missing operation basis")
	}
	switch action {
	case "acknowledge":
		if a.Acknowledged || c.Acknowledged {
			return errors.New("alarm is already acknowledged")
		}
	case "assign", "note":
	case "complete":
		if a.Active {
			return errors.New("alarm condition must recover before handling can complete")
		}
		if c.Status == "completed" {
			return errors.New("handling is already completed")
		}
		if !a.Acknowledged && !c.Acknowledged {
			return errors.New("acknowledge the alarm before completing handling")
		}
		if c.AssigneeID != "" && c.AssigneeID != p.User.ID && !includesHistory(p.User.Roles, "admin") {
			return errors.New("only the current assignee can complete handling")
		}
	case "reopen":
		if c.Status == "open" {
			return errors.New("handling is already open")
		}
	default:
		return errors.New("unknown alarm operation")
	}
	return nil
}

type AlarmActionInput struct {
	RequestID             string `json:"request_id" minLength:"1" maxLength:"200" required:"true"`
	ExpectedVersion       int64  `json:"expected_version" minimum:"0" required:"true"`
	ExpectedActionVersion string `json:"expected_action_version" minLength:"1" required:"true"`
	Action                string `json:"action" enum:"acknowledge,assign,note,complete,reopen" required:"true"`
	Reason                string `json:"reason" minLength:"1" maxLength:"4000" required:"true"`
	AssigneeID            string `json:"assignee_id,omitempty"`
}

func AlarmActionVersion(a model.Alarm) string {
	return store.Hash([]any{a.ID, a.DefinitionID, a.DefinitionVersion, a.EntityID, a.Severity, a.Active, a.Acknowledged, a.ClearedMS, a.Historical, a.RevisionStatus})
}

func (s *Business) ActOnAlarm(ctx context.Context, p identity.Principal, id string, input AlarmActionInput) (model.AlarmDetail, error) {
	var result model.AlarmDetail
	if err := businessReason(input.Reason); err != nil {
		return result, err
	}
	alarm, seen, err := s.alarm(ctx, p, id, "approve")
	if err != nil {
		return result, err
	}
	delete(seen.documents, documentKey{"alarm", id})
	c, err := s.Store.AlarmCase(ctx, alarm)
	if err != nil {
		return result, err
	}
	scope := "alarm:" + id + ":" + p.User.ID
	hash := store.Hash(input)
	actionErr := s.alarmAction(ctx, p, alarm, c, input.Action)
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		duplicate, e := tx.BusinessRequest(scope, input.RequestID, hash, &result)
		if e != nil || duplicate {
			return e
		}
		if c.Version != input.ExpectedVersion || AlarmActionVersion(alarm) != input.ExpectedActionVersion {
			return store.ErrConflict
		}
		if actionErr != nil {
			return actionErr
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if result.Alarm.ID != "" {
		return s.AlarmDetail(ctx, p, id)
	}
	if input.Action == "assign" {
		if _, err = s.member(ctx, input.AssigneeID, []string{alarm.EntityID}, seen); err != nil {
			return result, err
		}
	} else if input.AssigneeID != "" {
		return result, errors.New("assignee_id is only accepted for assign")
	}
	if err = seen.remember("alarm_case", id, c.Version); err != nil {
		return result, err
	}
	op := model.AlarmOperation{AlarmID: id, EntityID: alarm.EntityID, DefinitionID: alarm.DefinitionID, Version: 1, AlarmVersion: alarm.Version, AlarmActionVersion: input.ExpectedActionVersion, CaseVersion: c.Version, SourceID: s.NodeID, Action: input.Action, Reason: input.Reason, AssigneeID: input.AssigneeID, Actor: p.Actor, AtMS: s.Store.CurrentTime().UnixMilli(), Basis: []string{}}
	op.ID = "alarm-op:" + store.Hash([]string{s.NodeID, p.User.ID, input.RequestID, id})
	for _, entry := range c.Operations {
		op.Basis = append(op.Basis, entry.ID)
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		duplicate, e := tx.BusinessRequest(scope, input.RequestID, hash, &result)
		if e != nil || duplicate {
			return e
		}
		if e = seen.check(tx); e != nil {
			return e
		}
		current, e := tx.Get("alarm", id)
		if e != nil {
			return e
		}
		latest, e := store.Decode[model.Alarm](current)
		if e != nil {
			return e
		}
		if AlarmActionVersion(latest) != input.ExpectedActionVersion {
			return store.ErrConflict
		}
		op.AlarmVersion = latest.Version
		op.SourceSequence, e = tx.NextBusinessSequence()
		if e != nil {
			return e
		}
		updated, e := tx.ApplyAlarmOperation(op)
		if e != nil {
			return e
		}
		d, e := tx.Get("alarm", id)
		if e != nil {
			return e
		}
		a, e := store.Decode[model.Alarm](d)
		if e != nil {
			return e
		}
		result = model.AlarmDetail{Alarm: a, ActionVersion: AlarmActionVersion(a), Case: updated, AllowedActions: []model.BusinessAction{}}
		if e = tx.Audit(p.Actor, "alarm."+input.Action, alarm.EntityID, op.ID, op); e != nil {
			return e
		}
		return tx.CompleteBusinessRequest(scope, input.RequestID, hash, result)
	})
	if err != nil {
		return result, err
	}
	return s.AlarmDetail(ctx, p, id)
}

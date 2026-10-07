package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type CreateWorkOrderInput struct {
	RequestID    string   `json:"request_id" minLength:"1" maxLength:"200" required:"true"`
	ID           string   `json:"id" minLength:"1" maxLength:"200" required:"true"`
	Title        string   `json:"title" minLength:"1" maxLength:"200" required:"true"`
	Description  string   `json:"description,omitempty" maxLength:"8000"`
	GroupID      string   `json:"group_id" minLength:"1" required:"true"`
	AssigneeID   string   `json:"assignee_id" minLength:"1" required:"true"`
	AlarmIDs     []string `json:"alarm_ids,omitempty" maxItems:"50"`
	ExecutionIDs []string `json:"execution_ids,omitempty" maxItems:"50"`
	Reason       string   `json:"reason" minLength:"1" maxLength:"4000" required:"true"`
}

type WorkOrderActionInput struct {
	RequestID       string    `json:"request_id" minLength:"1" maxLength:"200" required:"true"`
	ExpectedVersion int64     `json:"expected_version" minimum:"1" required:"true"`
	Action          string    `json:"action" enum:"assign,start,update,note,complete,reopen" required:"true"`
	Reason          string    `json:"reason" minLength:"1" maxLength:"4000" required:"true"`
	AssigneeID      string    `json:"assignee_id,omitempty"`
	Title           *string   `json:"title,omitempty"`
	Description     *string   `json:"description,omitempty"`
	AlarmIDs        *[]string `json:"alarm_ids,omitempty" maxItems:"50"`
	ExecutionIDs    *[]string `json:"execution_ids,omitempty" maxItems:"50"`
}

type HandoverInput struct {
	RequestID       string                   `json:"request_id" minLength:"1" maxLength:"200" required:"true"`
	ExpectedVersion int64                    `json:"expected_version" minimum:"1" required:"true"`
	FromUserID      string                   `json:"from_user_id" minLength:"1" required:"true"`
	ToUserID        string                   `json:"to_user_id" minLength:"1" required:"true"`
	PendingItems    []string                 `json:"pending_items" minItems:"1" maxItems:"50" required:"true"`
	Evidence        []model.HandoverEvidence `json:"evidence,omitempty" maxItems:"50"`
	Reason          string                   `json:"reason" minLength:"1" maxLength:"4000" required:"true"`
}

func (s *Business) linkedResources(ctx context.Context, p identity.Principal, group string, alarms, executions []string, seen *revisions) ([]string, error) {
	if group == "" || len(alarms) > 50 || len(executions) > 50 {
		return nil, errors.New("a group and at most 50 alarm/execution references are required")
	}
	resources := []string{group}
	doc, err := s.read(ctx, "entity", group, seen)
	if err != nil {
		return nil, err
	}
	g, err := store.Decode[model.Entity](doc)
	if err != nil {
		return nil, err
	}
	if g.Kind != "asset" {
		return nil, errors.New("work order group must be an asset")
	}
	for _, id := range uniqueHistory(alarms) {
		doc, err := s.read(ctx, "alarm", id, seen)
		if err != nil {
			return nil, err
		}
		a, err := store.Decode[model.Alarm](doc)
		if err != nil {
			return nil, err
		}
		delete(seen.documents, documentKey{"alarm", id})
		alarmID, entityID, definitionID := id, a.EntityID, a.DefinitionID
		seen.checks = append(seen.checks, func(tx *store.Tx) error {
			d, e := tx.Get("alarm", alarmID)
			if e != nil {
				return e
			}
			current, e := store.Decode[model.Alarm](d)
			if e != nil {
				return e
			}
			if current.EntityID != entityID || current.DefinitionID != definitionID {
				return store.ErrConflict
			}
			return nil
		})
		resources = append(resources, a.EntityID)
		doc, err = s.read(ctx, "definition", a.DefinitionID, seen)
		if err != nil {
			return nil, err
		}
		d, err := store.Decode[model.Definition](doc)
		if err != nil {
			return nil, err
		}
		resources = append(resources, d.GroupID)
	}
	for _, id := range uniqueHistory(executions) {
		doc, err := s.read(ctx, "execution", id, seen)
		if err != nil {
			return nil, err
		}
		execution, err := store.Decode[model.Execution](doc)
		if err != nil {
			return nil, err
		}
		doc, err = s.read(ctx, "definition", execution.DefinitionID, seen)
		if err != nil {
			return nil, err
		}
		current, err := store.Decode[model.Definition](doc)
		if err != nil {
			return nil, err
		}
		resources = append(resources, current.GroupID)
		versions, err := s.Store.Versions(ctx, "definition", execution.DefinitionID)
		if err != nil {
			return nil, err
		}
		found := false
		for _, v := range versions {
			d, err := store.Decode[model.Definition](v)
			if err != nil {
				return nil, err
			}
			if d.Version != execution.DefinitionVersion {
				continue
			}
			found = true
			resources = append(resources, definitionResources(d)...)
		}
		if !found {
			return nil, errors.New("associated execution definition version is unavailable")
		}
		for _, sample := range execution.Snapshot {
			resources = append(resources, sample.DeviceID)
		}
		evidence, e := s.Store.ExecutionEvidence(ctx, execution.DownlinkID)
		if e != nil {
			return nil, e
		}
		for _, item := range evidence {
			resources = append(resources, item.DeviceID)
		}
		expectedEvidence := store.Hash(evidence)
		executionID := execution.DownlinkID
		seen.checks = append(seen.checks, func(tx *store.Tx) error {
			current, e := tx.ExecutionEvidence(executionID)
			if e != nil {
				return e
			}
			if store.Hash(current) != expectedEvidence {
				return store.ErrConflict
			}
			return nil
		})
	}
	resources = uniqueHistory(resources)
	for _, resource := range resources {
		if err = s.access().permit(ctx, p, "read", resource, seen); err != nil {
			return nil, err
		}
	}
	return resources, nil
}

func definitionResources(d model.Definition) []string {
	resources := []string{d.GroupID}
	resources = append(resources, d.Selector.DeviceIDs...)
	if d.Selector.AssetID != "" {
		resources = append(resources, d.Selector.AssetID)
	}
	for _, c := range d.Policy.Conditions {
		resources = append(resources, c.DeviceID)
	}
	for _, steps := range [][]model.Step{d.Policy.Steps, d.Policy.Degraded} {
		for _, step := range steps {
			resources = append(resources, step.DeviceID)
		}
	}
	result := []string{}
	for _, r := range uniqueHistory(resources) {
		if r != "" {
			result = append(result, r)
		}
	}
	return result
}

func workOrderText(title, description string) error {
	if strings.TrimSpace(title) == "" || len(title) > 200 || len(description) > 8000 {
		return errors.New("work order title must contain 1 to 200 characters and description at most 8000")
	}
	return nil
}

func (s *Business) CreateWorkOrder(ctx context.Context, p identity.Principal, input CreateWorkOrderInput) (model.WorkOrderDetail, error) {
	var result model.WorkOrder
	if err := businessID(input.ID); err != nil {
		return model.WorkOrderDetail{}, err
	}
	if err := businessReason(input.Reason); err != nil {
		return model.WorkOrderDetail{}, err
	}
	if err := workOrderText(input.Title, input.Description); err != nil {
		return model.WorkOrderDetail{}, err
	}
	seen, err := s.authorize(ctx, p, "approve", []string{input.GroupID})
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	resources, err := s.linkedResources(ctx, p, input.GroupID, input.AlarmIDs, input.ExecutionIDs, seen)
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	member, err := s.member(ctx, input.AssigneeID, resources, seen)
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	now := s.Store.CurrentTime().UnixMilli()
	result = model.WorkOrder{ID: input.ID, Title: input.Title, Description: input.Description, GroupID: input.GroupID, Status: "open", AssigneeID: member.ID, AssigneeDepartmentID: member.DepartmentID, AssigneeVersion: member.Version, AlarmIDs: uniqueHistory(input.AlarmIDs), ExecutionIDs: uniqueHistory(input.ExecutionIDs), CreatedBy: p.User.ID, CreatedMS: now, UpdatedMS: now, Version: 1, Entries: []model.BusinessEntry{{ID: input.RequestID, Action: "create", Reason: input.Reason, Actor: p.Actor, AtMS: now, Version: 1}}}
	scope := "work-order-create:" + p.User.ID
	hash := store.Hash(input)
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		duplicate, e := tx.BusinessRequest(scope, input.RequestID, hash, &result)
		if e != nil || duplicate {
			return e
		}
		if e = seen.check(tx); e != nil {
			return e
		}
		if _, e = tx.Put("work_order", result.ID, 0, result); e != nil {
			return e
		}
		if e = tx.Audit(p.Actor, "work_order.create", result.GroupID, result.ID, result); e != nil {
			return e
		}
		return tx.CompleteBusinessRequest(scope, input.RequestID, hash, result)
	})
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	return s.WorkOrderDetail(ctx, p, result.ID)
}

func (s *Business) workOrder(ctx context.Context, p identity.Principal, id, action string) (model.WorkOrder, *revisions, []string, error) {
	seen, err := s.authorize(ctx, p, action, nil)
	if err != nil {
		return model.WorkOrder{}, nil, nil, err
	}
	doc, err := s.read(ctx, "work_order", id, seen)
	if err != nil {
		return model.WorkOrder{}, nil, nil, err
	}
	w, err := store.Decode[model.WorkOrder](doc)
	if err != nil {
		return w, nil, nil, err
	}
	if err = s.access().permit(ctx, p, action, w.GroupID, seen); err != nil {
		return w, nil, nil, err
	}
	resources, err := s.linkedResources(ctx, p, w.GroupID, w.AlarmIDs, w.ExecutionIDs, seen)
	return w, seen, resources, err
}

func (s *Business) responsibility(ctx context.Context, w model.WorkOrder, resources []string, seen *revisions) error {
	u, err := s.member(ctx, w.AssigneeID, resources, seen)
	if err != nil {
		return err
	}
	if u.DepartmentID != w.AssigneeDepartmentID {
		return errors.New("assignee department changed; assign the work order again after review")
	}
	return nil
}

func workOrderAction(p identity.Principal, w model.WorkOrder, action string, responsibility error) error {
	if action == "assign" {
		if w.Status == "completed" {
			return errors.New("reopen the work order before reassignment")
		}
		return nil
	}
	if w.AssigneeID != p.User.ID && !includesHistory(p.User.Roles, "admin") {
		return errors.New("operation requires the current assignee or an administrator")
	}
	if responsibility != nil {
		return responsibility
	}
	switch action {
	case "start":
		if w.Status != "open" {
			return errors.New("only an open work order can start")
		}
	case "update", "handover", "complete":
		if w.Status == "completed" {
			return errors.New("work order is completed")
		}
	case "reopen":
		if w.Status != "completed" {
			return errors.New("work order is already open")
		}
	case "note":
	default:
		return errors.New("unknown work order operation")
	}
	return nil
}

func (s *Business) WorkOrderDetail(ctx context.Context, p identity.Principal, id string) (model.WorkOrderDetail, error) {
	w, seen, resources, err := s.workOrder(ctx, p, id, "read")
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	detail := model.WorkOrderDetail{WorkOrder: w, Handovers: []model.Handover{}, AllowedActions: []model.BusinessAction{}}
	responsibility := s.responsibility(ctx, w, resources, seen)
	if responsibility != nil {
		detail.ResponsibilityIssue = responsibility.Error()
	}
	for _, action := range []string{"assign", "start", "update", "note", "complete", "reopen", "handover"} {
		e := s.Identity.Permit(ctx, p, "approve", w.GroupID)
		if e == nil {
			e = workOrderAction(p, w, action, responsibility)
		}
		a := model.BusinessAction{Action: action, Allowed: e == nil}
		if e != nil {
			a.Reason = e.Error()
		}
		detail.AllowedActions = append(detail.AllowedActions, a)
	}
	docs, err := s.Store.List(ctx, "handover")
	if err != nil {
		return detail, err
	}
	for _, doc := range docs {
		h, e := store.Decode[model.Handover](doc)
		if e != nil {
			return detail, e
		}
		if h.WorkOrderID == id {
			detail.Handovers = append(detail.Handovers, h)
		}
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
	return detail, err
}

func (s *Business) WorkOrders(ctx context.Context, p identity.Principal) ([]model.WorkOrder, error) {
	if _, err := s.authorize(ctx, p, "read", nil); err != nil {
		return nil, err
	}
	docs, err := s.Store.List(ctx, "work_order")
	if err != nil {
		return nil, err
	}
	if len(docs) > 5000 {
		return nil, errors.New("work order collection exceeds the interactive budget")
	}
	result := []model.WorkOrder{}
	for _, d := range docs {
		w, _, _, e := s.workOrder(ctx, p, d.ID, "read")
		if errors.Is(e, identity.ErrDenied) || errors.Is(e, store.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		result = append(result, w)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedMS != result[j].UpdatedMS {
			return result[i].UpdatedMS > result[j].UpdatedMS
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

func (s *Business) ChangeWorkOrder(ctx context.Context, p identity.Principal, id string, input WorkOrderActionInput) (model.WorkOrderDetail, error) {
	if err := businessReason(input.Reason); err != nil {
		return model.WorkOrderDetail{}, err
	}
	w, seen, resources, err := s.workOrder(ctx, p, id, "approve")
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	responsibility := s.responsibility(ctx, w, resources, seen)
	scope := "work-order:" + id + ":" + p.User.ID
	hash := store.Hash(input)
	var replay model.WorkOrder
	err = s.Store.Write(ctx, func(tx *store.Tx) error { _, e := tx.BusinessRequest(scope, input.RequestID, hash, &replay); return e })
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	if replay.ID != "" {
		return s.WorkOrderDetail(ctx, p, id)
	}
	if input.ExpectedVersion != w.Version {
		return model.WorkOrderDetail{}, store.ErrConflict
	}
	if err = workOrderAction(p, w, input.Action, responsibility); err != nil {
		return model.WorkOrderDetail{}, err
	}
	if input.Action != "assign" && input.AssigneeID != "" {
		return model.WorkOrderDetail{}, errors.New("assignee_id requires assign")
	}
	if input.Action != "update" && (input.Title != nil || input.Description != nil || input.AlarmIDs != nil || input.ExecutionIDs != nil) {
		return model.WorkOrderDetail{}, errors.New("content fields require update")
	}
	switch input.Action {
	case "assign":
		u, e := s.member(ctx, input.AssigneeID, resources, seen)
		if e != nil {
			return model.WorkOrderDetail{}, e
		}
		w.AssigneeID = u.ID
		w.AssigneeDepartmentID = u.DepartmentID
		w.AssigneeVersion = u.Version
	case "start":
		w.Status = "in_progress"
	case "complete":
		w.Status = "completed"
		w.CompletedMS = s.Store.CurrentTime().UnixMilli()
	case "reopen":
		w.Status = "open"
		w.CompletedMS = 0
	case "update":
		if input.Title != nil {
			w.Title = *input.Title
		}
		if input.Description != nil {
			w.Description = *input.Description
		}
		if input.AlarmIDs != nil {
			w.AlarmIDs = uniqueHistory(*input.AlarmIDs)
		}
		if input.ExecutionIDs != nil {
			w.ExecutionIDs = uniqueHistory(*input.ExecutionIDs)
		}
		if e := workOrderText(w.Title, w.Description); e != nil {
			return model.WorkOrderDetail{}, e
		}
		resources, err = s.linkedResources(ctx, p, w.GroupID, w.AlarmIDs, w.ExecutionIDs, seen)
		if err != nil {
			return model.WorkOrderDetail{}, err
		}
		if _, err = s.member(ctx, w.AssigneeID, resources, seen); err != nil {
			return model.WorkOrderDetail{}, err
		}
	}
	if len(w.Entries) >= 1000 {
		return model.WorkOrderDetail{}, errors.New("work order operation limit is 1000")
	}
	w.Version++
	w.UpdatedMS = s.Store.CurrentTime().UnixMilli()
	entry := model.BusinessEntry{ID: input.RequestID, Action: input.Action, Reason: input.Reason, Actor: p.Actor, AtMS: w.UpdatedMS, BeforeVersion: input.ExpectedVersion, Version: w.Version}
	w.Entries = append(w.Entries, entry)
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		duplicate, e := tx.BusinessRequest(scope, input.RequestID, hash, &replay)
		if e != nil || duplicate {
			return e
		}
		if e = seen.check(tx); e != nil {
			return e
		}
		if _, e = tx.Put("work_order", id, input.ExpectedVersion, w); e != nil {
			return e
		}
		if e = tx.Audit(p.Actor, "work_order."+input.Action, w.GroupID, id, entry); e != nil {
			return e
		}
		return tx.CompleteBusinessRequest(scope, input.RequestID, hash, w)
	})
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	return s.WorkOrderDetail(ctx, p, id)
}

func (s *Business) HandoverWorkOrder(ctx context.Context, p identity.Principal, id string, input HandoverInput) (model.WorkOrderDetail, error) {
	if err := businessReason(input.Reason); err != nil {
		return model.WorkOrderDetail{}, err
	}
	if len(input.PendingItems) == 0 || len(input.PendingItems) > 50 || len(input.Evidence) > 50 {
		return model.WorkOrderDetail{}, errors.New("handover requires 1 to 50 pending items and at most 50 evidence references")
	}
	for _, item := range input.PendingItems {
		if err := businessReason(item); err != nil {
			return model.WorkOrderDetail{}, err
		}
	}
	w, seen, resources, err := s.workOrder(ctx, p, id, "approve")
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	if len(w.Entries) >= 500 {
		return model.WorkOrderDetail{}, errors.New("work order entry limit is 500")
	}
	scope := "handover:" + id + ":" + p.User.ID
	hash := store.Hash(input)
	var replay model.Handover
	err = s.Store.Write(ctx, func(tx *store.Tx) error { _, e := tx.BusinessRequest(scope, input.RequestID, hash, &replay); return e })
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	if replay.ID != "" {
		return s.WorkOrderDetail(ctx, p, id)
	}
	if w.Version != input.ExpectedVersion || w.AssigneeID != input.FromUserID {
		return model.WorkOrderDetail{}, store.ErrConflict
	}
	if input.ToUserID == input.FromUserID {
		return model.WorkOrderDetail{}, errors.New("handover requires another member")
	}
	if err = workOrderAction(p, w, "handover", s.responsibility(ctx, w, resources, seen)); err != nil {
		return model.WorkOrderDetail{}, err
	}
	to, err := s.member(ctx, input.ToUserID, resources, seen)
	if err != nil {
		return model.WorkOrderDetail{}, err
	}
	for _, ev := range input.Evidence {
		if ev.Version < 1 {
			return model.WorkOrderDetail{}, errors.New("evidence requires its reviewed version")
		}
		var doc store.Document
		switch ev.Kind {
		case "alarm":
			if !includesHistory(w.AlarmIDs, ev.ID) {
				return model.WorkOrderDetail{}, errors.New("evidence alarm must be associated with the work order")
			}
			doc, err = s.read(ctx, "alarm", ev.ID, seen)
			if err == nil {
				var a model.Alarm
				a, err = store.Decode[model.Alarm](doc)
				if err == nil {
					if ev.ActionVersion == "" {
						if a.Version != ev.Version {
							err = store.ErrConflict
						}
					} else {
						if AlarmActionVersion(a) != ev.ActionVersion {
							err = store.ErrConflict
						}
						delete(seen.documents, documentKey{"alarm", ev.ID})
						alarmID, expected := ev.ID, ev.ActionVersion
						seen.checks = append(seen.checks, func(tx *store.Tx) error {
							d, e := tx.Get("alarm", alarmID)
							if e != nil {
								return e
							}
							a, e := store.Decode[model.Alarm](d)
							if e != nil {
								return e
							}
							if AlarmActionVersion(a) != expected {
								return store.ErrConflict
							}
							return nil
						})
					}
				}
			}
		case "execution":
			if !includesHistory(w.ExecutionIDs, ev.ID) {
				return model.WorkOrderDetail{}, errors.New("evidence execution must be associated with the work order")
			}
			doc, err = s.read(ctx, "execution", ev.ID, seen)
			if err == nil {
				var e model.Execution
				e, err = store.Decode[model.Execution](doc)
				if err == nil && e.Version != ev.Version {
					err = store.ErrConflict
				}
			}
		case "work_order":
			if ev.ID != w.ID || ev.Version != w.Version {
				err = store.ErrConflict
			}
		default:
			err = errors.New("unknown handover evidence kind")
		}
		if err != nil {
			return model.WorkOrderDetail{}, err
		}
	}
	now := s.Store.CurrentTime().UnixMilli()
	h := model.Handover{ID: "handover:" + store.Hash([]string{id, p.User.ID, input.RequestID}), WorkOrderID: id, GroupID: w.GroupID, FromUserID: w.AssigneeID, ToUserID: to.ID, FromDepartmentID: w.AssigneeDepartmentID, ToDepartmentID: to.DepartmentID, PendingItems: input.PendingItems, Evidence: input.Evidence, Reason: input.Reason, Actor: p.Actor, AtMS: now, WorkOrderVersion: w.Version + 1, Version: 1}
	w.AssigneeID = to.ID
	w.AssigneeDepartmentID = to.DepartmentID
	w.AssigneeVersion = to.Version
	w.Version++
	w.UpdatedMS = now
	w.Entries = append(w.Entries, model.BusinessEntry{ID: input.RequestID, Action: "handover", Reason: input.Reason, Actor: p.Actor, AtMS: now, BeforeVersion: input.ExpectedVersion, Version: w.Version})
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		duplicate, e := tx.BusinessRequest(scope, input.RequestID, hash, &replay)
		if e != nil || duplicate {
			return e
		}
		if e = seen.check(tx); e != nil {
			return e
		}
		if _, e = tx.Put("work_order", id, input.ExpectedVersion, w); e != nil {
			return e
		}
		if _, e = tx.Put("handover", h.ID, 0, h); e != nil {
			return e
		}
		if e = tx.Audit(p.Actor, "work_order.handover", w.GroupID, h.ID, h); e != nil {
			return e
		}
		return tx.CompleteBusinessRequest(scope, input.RequestID, hash, h)
	})
	if err != nil {
		return model.WorkOrderDetail{}, fmt.Errorf("handover commit: %w", err)
	}
	return s.WorkOrderDetail(ctx, p, id)
}

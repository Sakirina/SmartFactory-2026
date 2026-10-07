package application

import (
	"context"
	"errors"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Tasks struct {
	Store    TaskRepository
	Identity *identity.Manager
}
type TaskList struct {
	Items []model.Task `json:"items"`
	Next  string       `json:"next"`
}

func (s *Tasks) authorize(ctx context.Context, p identity.Principal, task model.Task, action string) (*revisions, error) {
	guard := newRevisions()
	access := &authorizationReader{Store: s.Store, Identity: s.Identity}
	if err := access.captureAuthorization(ctx, p, guard); err != nil {
		return nil, err
	}
	if err := access.permit(ctx, p, action, "", guard); err != nil {
		return nil, err
	}
	for _, resource := range task.Resources {
		if resource == "" {
			return nil, identity.ErrDenied
		}
		if err := access.permit(ctx, p, action, resource, guard); err != nil {
			return nil, err
		}
	}
	return guard, nil
}
func (s *Tasks) actions(ctx context.Context, p identity.Principal, task model.Task) model.Task {
	task.AllowedActions = []string{}
	if _, err := s.authorize(ctx, p, task, "register"); err != nil {
		return task
	}
	if task.CancelRequestedMS == 0 {
		switch task.State {
		case "available", "scheduled", "pending", "retryable", "running":
			task.AllowedActions = append(task.AllowedActions, "cancel")
		}
	}
	if !task.Superseded && task.BusinessState != "completed" {
		switch task.State {
		case "discarded", "cancelled", "retryable":
			task.AllowedActions = append(task.AllowedActions, "retry")
		}
	}
	return task
}
func (s *Tasks) Get(ctx context.Context, p identity.Principal, id string) (model.Task, error) {
	task, err := s.Store.Task(ctx, id)
	if err != nil {
		return task, err
	}
	if _, err = s.authorize(ctx, p, task, "read"); err != nil {
		return model.Task{}, err
	}
	return s.actions(ctx, p, task), nil
}
func (s *Tasks) List(ctx context.Context, p identity.Principal, after string, limit int) (TaskList, error) {
	result := TaskList{Items: []model.Task{}}
	ids, err := s.Store.TaskIDs(ctx, after, limit)
	if err != nil {
		return result, err
	}
	for _, id := range ids {
		result.Next = id
		task, err := s.Get(ctx, p, id)
		if errors.Is(err, identity.ErrDenied) || errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, task)
	}
	if len(ids) < limit {
		result.Next = ""
	}
	return result, nil
}
func (s *Tasks) Change(ctx context.Context, p identity.Principal, id, expected, action string) (model.Task, error) {
	task, err := s.Store.Task(ctx, id)
	if err != nil {
		return task, err
	}
	guards, err := s.authorize(ctx, p, task, "register")
	if err != nil {
		return model.Task{}, err
	}
	result, err := s.Store.ChangeTask(ctx, id, expected, action, p.Actor, guards.check)
	if err != nil {
		return result, err
	}
	return s.actions(ctx, p, result), nil
}

// RetryJob preserves the legacy HTTP Job response while using the same task
// retry and authorization path as the task API.
func (s *Tasks) RetryJob(ctx context.Context, p identity.Principal, id string) (model.Job, error) {
	doc, err := s.Store.Get(ctx, "job", id)
	if err != nil {
		return model.Job{}, err
	}
	job, err := store.Decode[model.Job](doc)
	if err != nil {
		return job, err
	}
	if job.TaskID != "" {
		task, err := s.Store.Task(ctx, job.TaskID)
		if err != nil {
			return model.Job{}, err
		}
		guard, err := s.authorize(ctx, p, task, "register")
		if err != nil {
			return model.Job{}, err
		}
		if job.Kind != "recompute" || job.Status != "failed" || task.Superseded || task.Kind != "recompute" || task.BusinessID != id {
			return model.Job{}, store.ErrConflict
		}
		if _, err := s.Store.ChangeTask(ctx, task.ID, task.Version, "retry", p.Actor, guard.check); err != nil {
			return model.Job{}, err
		}
		doc, err := s.Store.Get(ctx, "job", id)
		if err != nil {
			return model.Job{}, err
		}
		result, err := store.Decode[model.Job](doc)
		if err == nil && result.TaskID != task.ID {
			return model.Job{}, store.ErrConflict
		}
		result.Version = doc.Version
		return result, err
	}
	var definition *store.Document
	var definitionVersion int64
	if job.DefinitionID != "" {
		doc, err := s.Store.Get(ctx, "definition", job.DefinitionID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return model.Job{}, err
		}
		if err == nil {
			definition, definitionVersion = &doc, doc.Version
		}
	}
	resources, err := store.RecomputeTaskResources(job, definition)
	if err != nil {
		return model.Job{}, err
	}
	guard, err := s.authorize(ctx, p, model.Task{Resources: resources}, "register")
	if err != nil {
		return model.Job{}, err
	}
	if job.Kind != "recompute" || job.Status != "failed" {
		return model.Job{}, store.ErrConflict
	}
	if job.DefinitionID != "" {
		if err := guard.remember("definition", job.DefinitionID, definitionVersion); err != nil {
			return model.Job{}, err
		}
	}
	return s.Store.AdoptFailedRecompute(ctx, id, doc.Version, p.Actor, guard.check)
}

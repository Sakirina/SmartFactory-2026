package engine

import (
	"context"
	"errors"
	"fmt"

	"competition2026/product/platform/internal/compiledplan"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

var ErrPlanIsolated = errors.New("definition has an isolated execution plan")

type PlanIssue = compiledplan.PlanIssue

type PlanPreparation struct {
	Prepared int         `json:"prepared"`
	Isolated []PlanIssue `json:"isolated"`
}

// PrepareDefinition is a cold preparation operation for trusted imports and
// existing installations. New publications prepare within definitioncommit.
func (s *Service) PrepareDefinition(ctx context.Context, definition model.Definition) error {
	var prepared *model.ExecutionPlan
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		var err error
		prepared, _, err = compiledplan.Prepare(tx, definition, false)
		return err
	})
	if err != nil {
		return err
	}
	s.plans.Store(prepared.ID, prepared)
	return nil
}

// PreparePublishedPlans runs before serving requests or starting workers. Each
// invalid legacy version is isolated with a persisted diagnostic; valid versions
// remain available. Corrupt persisted plans return a startup error.
func (s *Service) PreparePublishedPlans(ctx context.Context) (PlanPreparation, error) {
	result := PlanPreparation{Isolated: []PlanIssue{}}
	documents, err := s.Store.List(ctx, "definition")
	if err != nil {
		return result, err
	}
	for _, current := range documents {
		versions, err := s.Store.Versions(ctx, "definition", current.ID)
		if err != nil {
			return result, err
		}
		for _, document := range versions {
			definition, err := store.Decode[model.Definition](document)
			if err != nil {
				return result, err
			}
			if definition.Status != "published" {
				continue
			}
			var plan *model.ExecutionPlan
			var issue *PlanIssue
			err = s.Store.Write(ctx, func(tx *store.Tx) error {
				var err error
				plan, issue, err = compiledplan.Prepare(tx, definition, true)
				return err
			})
			if err != nil {
				return result, err
			}
			if issue != nil {
				s.plans.Delete(issue.ID)
				result.Isolated = append(result.Isolated, *issue)
			} else {
				s.plans.Store(plan.ID, plan)
				result.Prepared++
			}
		}
	}
	return result, nil
}

func (s *Service) loadPlan(ctx context.Context, definition model.Definition) (*model.ExecutionPlan, error) {
	content, err := rulecore.ContentHash(definition)
	if err != nil {
		return nil, err
	}
	id := rulecore.PlanID(definition, content)
	if cached, exists := s.plans.Load(id); exists {
		return cached.(*model.ExecutionPlan), nil
	}
	plan, err := compiledplan.Load(ctx, s.Store, definition)
	if errors.Is(err, store.ErrNotFound) {
		if _, issueErr := s.Store.Get(ctx, "plan_issue", id); issueErr == nil {
			return nil, fmt.Errorf("%w: %s version %d", ErrPlanIsolated, definition.ID, definition.Version)
		} else if !errors.Is(issueErr, store.ErrNotFound) {
			return nil, issueErr
		}
		return nil, fmt.Errorf("execution plan has not been prepared: %s version %d: %w", definition.ID, definition.Version, err)
	}
	if err != nil {
		return nil, err
	}
	s.plans.Store(id, plan)
	return plan, nil
}

func (s *Service) prepareDraft(ctx context.Context, definition model.Definition) (*model.ExecutionPlan, error) {
	at, _ := ctx.Value(historicalAtKey{}).(int64)
	if err := compiledplan.CheckDependencies(ctx, definition, at, func(ctx context.Context, id string, at int64) (model.Definition, error) {
		other, err := s.Published(ctx, id, at)
		if err != nil {
			return other, err
		}
		if _, err = s.loadPlan(ctx, other); err != nil {
			return other, err
		}
		return other, nil
	}); err != nil {
		return nil, err
	}
	return compiledplan.Compile(ctx, definition)
}

// Package compiledplan persists immutable rule plans for publication and import.
package compiledplan

import (
	"context"
	"errors"
	"fmt"

	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func Compile(ctx context.Context, definition model.Definition) (*model.ExecutionPlan, error) {
	plan, validation := rulecore.Compile(ctx, definition)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validation.Valid {
		return nil, &DefinitionError{Code: "definition_compilation", Issues: validation.Errors}
	}
	return plan, nil
}

func Put(tx *store.Tx, definition model.Definition, plan *model.ExecutionPlan) error {
	if err := rulecore.Verify(plan, definition); err != nil {
		return err
	}
	doc, err := tx.Get("compiled_plan", plan.ID)
	if err == nil {
		existing, err := store.Decode[model.ExecutionPlan](doc)
		if err != nil {
			return err
		}
		if err := rulecore.Verify(&existing, definition); err != nil {
			return err
		}
		if existing.SHA256 != plan.SHA256 {
			return errors.New("immutable execution plan already has different content")
		}
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	_, err = tx.Put("compiled_plan", plan.ID, 0, plan)
	return err
}

// Import prepares older wire documents before they become visible to workers.
// New documents carry the exact plan compiled by their publishing authority.
func Import(tx *store.Tx, definition model.Definition) error {
	_, _, err := Prepare(tx, definition, true)
	return err
}

func Load(ctx context.Context, database *store.Store, definition model.Definition) (*model.ExecutionPlan, error) {
	return loadWith(definition, func(id string) (store.Document, error) { return database.Get(ctx, "compiled_plan", id) })
}

func loadWith(definition model.Definition, get func(string) (store.Document, error)) (*model.ExecutionPlan, error) {
	content, err := rulecore.ContentHash(definition)
	if err != nil {
		return nil, err
	}
	doc, err := get(rulecore.PlanID(definition, content))
	if err != nil {
		return nil, err
	}
	plan, err := store.Decode[model.ExecutionPlan](doc)
	if err != nil {
		return nil, fmt.Errorf("corrupt stored execution plan: %w", err)
	}
	if err = rulecore.Verify(&plan, definition); err != nil {
		return nil, fmt.Errorf("corrupt stored execution plan: %w", err)
	}
	return &plan, nil
}

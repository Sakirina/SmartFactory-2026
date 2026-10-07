package compiledplan

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// DefinitionError identifies authoring or dependency failures that can isolate
// a legacy definition. Storage, cancellation and plan-integrity errors remain
// ordinary failures and must abort the preparation transaction.
type DefinitionError struct {
	Code   string
	Issues []string
}

func (e *DefinitionError) Error() string { return e.Code + ": " + strings.Join(e.Issues, "; ") }

type PlanIssue struct {
	ID                string   `json:"id"`
	DefinitionID      string   `json:"definition_id"`
	DefinitionVersion int64    `json:"definition_version"`
	ContentSHA256     string   `json:"content_sha256"`
	Code              string   `json:"code"`
	Errors            []string `json:"errors"`
	AtMS              int64    `json:"at_ms"`
}

type Resolver func(context.Context, string, int64) (model.Definition, error)

// CheckDependencies preserves storage failures and checks every dependency at
// the caller's explicit definition time. The resolver supplies published plans.
func CheckDependencies(ctx context.Context, definition model.Definition, at int64, resolve Resolver) error {
	visiting := map[string]bool{definition.ID: true}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if visiting[id] {
			return &DefinitionError{Code: "dependency_cycle", Issues: []string{id}}
		}
		if visited[id] {
			return nil
		}
		other, err := resolve(ctx, id, at)
		if errors.Is(err, store.ErrNotFound) {
			return &DefinitionError{Code: "dependency_unavailable", Issues: []string{id}}
		}
		if err != nil {
			return err
		}
		if other.Status != "published" {
			return &DefinitionError{Code: "dependency_unavailable", Issues: []string{id}}
		}
		visiting[id] = true
		for _, nested := range other.Dependencies {
			if err := visit(nested); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for _, id := range definition.Dependencies {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func definitionAt(tx *store.Tx, id string, at int64) (model.Definition, error) {
	rows, err := tx.QueryContext(tx.Ctx, "SELECT data FROM document_versions WHERE kind='definition' AND id=$1 ORDER BY version", id)
	if err != nil {
		return model.Definition{}, err
	}
	defer rows.Close()
	var found model.Definition
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return found, err
		}
		var definition model.Definition
		if err := store.DecodeJSON([]byte(raw), &definition); err != nil {
			return found, err
		}
		if at == 0 || definition.EffectiveMS <= at {
			found = definition
		}
	}
	if err := rows.Err(); err != nil {
		return found, err
	}
	if found.ID == "" {
		return found, store.ErrNotFound
	}
	return found, nil
}

// Prepare validates a complete dependency graph against the transaction's
// imported document history. It preserves source definition identities, creates
// only missing immutable plans and clears an earlier issue after recovery.
func Prepare(tx *store.Tx, definition model.Definition, isolateLegacy bool) (*model.ExecutionPlan, *PlanIssue, error) {
	type item struct {
		definition model.Definition
		plan       *model.ExecutionPlan
	}
	prepared := map[string]item{}
	visiting := map[string]bool{}
	var visit func(model.Definition, int64) (*model.ExecutionPlan, error)
	visit = func(current model.Definition, at int64) (*model.ExecutionPlan, error) {
		if err := tx.Ctx.Err(); err != nil {
			return nil, err
		}
		if visiting[current.ID] {
			return nil, &DefinitionError{Code: "dependency_cycle", Issues: []string{current.ID}}
		}
		content, err := rulecore.ContentHash(current)
		if err != nil {
			return nil, err
		}
		id := rulecore.PlanID(current, content)
		if existing, ok := prepared[id]; ok {
			return existing.plan, nil
		}
		plan, err := loadWith(current, func(id string) (store.Document, error) { return tx.Get("compiled_plan", id) })
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if plan == nil {
			plan = current.ExecutionPlan
			if plan == nil {
				plan, err = Compile(tx.Ctx, current)
			} else {
				err = rulecore.Verify(plan, current)
			}
			if err != nil {
				return nil, err
			}
		}
		// A supplied plan must be checked even when a local plan already exists.
		if current.ExecutionPlan != nil {
			if err := rulecore.Verify(current.ExecutionPlan, current); err != nil {
				return nil, err
			}
			if plan.SHA256 != current.ExecutionPlan.SHA256 {
				return nil, errors.New("immutable execution plan already has different content")
			}
		}
		visiting[current.ID] = true
		for _, dependency := range current.Dependencies {
			other, err := definitionAt(tx, dependency, at)
			if errors.Is(err, store.ErrNotFound) || err == nil && other.Status != "published" {
				return nil, &DefinitionError{Code: "dependency_unavailable", Issues: []string{dependency}}
			}
			if err != nil {
				return nil, err
			}
			if _, err = visit(other, at); err != nil {
				return nil, err
			}
		}
		delete(visiting, current.ID)
		prepared[id] = item{current, plan}
		return plan, nil
	}
	plan, err := visit(definition, definition.EffectiveMS)
	if err != nil {
		var invalid *DefinitionError
		if !isolateLegacy || definition.ExecutionPlan != nil || !errors.As(err, &invalid) {
			return nil, nil, err
		}
		content, hashErr := rulecore.ContentHash(definition)
		if hashErr != nil {
			return nil, nil, hashErr
		}
		issue := &PlanIssue{ID: rulecore.PlanID(definition, content), DefinitionID: definition.ID, DefinitionVersion: definition.Version, ContentSHA256: content, Code: invalid.Code, Errors: append([]string{}, invalid.Issues...), AtMS: tx.Store.Now().UnixMilli()}
		if _, err = tx.Put("plan_issue", issue.ID, -1, issue); err != nil {
			return nil, nil, err
		}
		return nil, issue, nil
	}
	ids := make([]string, 0, len(prepared))
	for id := range prepared {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		item := prepared[id]
		if err := Put(tx, item.definition, item.plan); err != nil {
			return nil, nil, err
		}
		if err := tx.Delete("plan_issue", id); err != nil {
			return nil, nil, err
		}
	}
	return plan, nil, nil
}

// RetryIssues prepares legacy documents whose dependencies arrived in a later
// metadata batch. It never changes the original definition or source version.
func RetryIssues(tx *store.Tx) error {
	rows, err := tx.QueryContext(tx.Ctx, "SELECT data FROM documents WHERE kind='plan_issue' ORDER BY id")
	if err != nil {
		return err
	}
	issues := []PlanIssue{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var issue PlanIssue
		if err := store.DecodeJSON([]byte(raw), &issue); err != nil {
			rows.Close()
			return err
		}
		issues = append(issues, issue)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, issue := range issues {
		var raw string
		err = tx.QueryRowContext(tx.Ctx, "SELECT data FROM document_versions WHERE kind='definition' AND id=$1 AND version=$2", issue.DefinitionID, issue.DefinitionVersion).Scan(&raw)
		if err != nil {
			return err
		}
		var definition model.Definition
		if err := store.DecodeJSON([]byte(raw), &definition); err != nil {
			return err
		}
		if _, _, err = Prepare(tx, definition, true); err != nil {
			return fmt.Errorf("retry plan %s: %w", issue.ID, err)
		}
	}
	return nil
}

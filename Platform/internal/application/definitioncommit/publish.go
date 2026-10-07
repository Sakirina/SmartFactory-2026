// Package definitioncommit commits a prepared publication for every entry point.
package definitioncommit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"competition2026/product/platform/internal/compiledplan"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Revision = store.Revision

type Prepared struct {
	Draft        model.Draft
	DraftVersion int64
	Validation   model.Validation
	Revisions    []Revision
}

func CheckRevisions(tx *store.Tx, revisions []Revision) error {
	return tx.CheckRevisions(revisions)
}

// Commit is the single publication write path for authenticated use cases and
// trusted initialization. Callers supply the exact draft and dependency versions
// they authorized and validated. Every publication artifact is committed here.
type Repository interface {
	CurrentTime() time.Time
	Write(context.Context, func(*store.Tx) error) error
}

func Commit(ctx context.Context, database Repository, actor model.Actor, prepared Prepared) (model.Definition, error) {
	draft := prepared.Draft
	prepared.Revisions = append(append([]Revision{}, prepared.Revisions...), Revision{Kind: "draft", ID: draft.ID, Version: prepared.DraftVersion})
	d := draft.Definition
	validation := prepared.Validation
	if !validation.Valid {
		return model.Definition{}, fmt.Errorf("definition validation: %s", strings.Join(validation.Errors, "; "))
	}
	d.Status = "published"
	d.Version = draft.BaseVersion + 1
	d.EffectiveMS = database.CurrentTime().UnixMilli()
	plan, err := compiledplan.Compile(ctx, d)
	if err != nil {
		return model.Definition{}, err
	}
	d.ExecutionPlan = plan
	err = database.Write(ctx, func(tx *store.Tx) error {
		if err := CheckRevisions(tx, prepared.Revisions); err != nil {
			return err
		}
		if _, _, err := compiledplan.Prepare(tx, d, false); err != nil {
			return err
		}
		if _, err := tx.Put("definition", d.ID, draft.BaseVersion, d); err != nil {
			return err
		}
		for _, output := range d.Outputs {
			if _, err := tx.Put("catalogue", d.ID+"."+output.Key, -1, map[string]any{"id": d.ID + "." + output.Key, "definition_id": d.ID, "version": d.Version, "name": d.Name, "kind": d.Kind, "field": output, "selector": d.Selector, "group_id": d.GroupID, "status": "active", "query_path": "/api/sf/v1/data"}); err != nil {
				return err
			}
		}
		draft.Definition = d
		draft.BaseVersion = d.Version
		draft.Version = prepared.DraftVersion + 1
		draft.UpdatedMS = d.EffectiveMS
		if _, err := tx.Put("draft", draft.ID, draft.Version-1, draft); err != nil {
			return err
		}
		if err := tx.Enqueue(fmt.Sprintf("tb-definition:%s:%d", d.ID, d.Version), "tb_definition", d.ID, d); err != nil {
			return err
		}
		if err := tx.Enqueue(fmt.Sprintf("definition-sync:%s:%d", d.ID, d.Version), "edge_definition", d.ID, d); err != nil {
			return err
		}
		return tx.Audit(actor, "definition.publish", d.ID, draft.ID, map[string]any{"definition": d, "validation": validation})
	})
	return d, err
}

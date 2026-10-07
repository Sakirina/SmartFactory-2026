// Package application contains authenticated use cases shared by transport adapters.
package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"competition2026/product/platform/internal/application/definitioncommit"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type SaveDraftInput struct {
	Draft           model.Draft `json:"draft"`
	ExpectedVersion int64       `json:"expected_version" minimum:"0"`
}

// DraftInput optionally pins the revision a caller has reviewed. Omission keeps
// existing clients compatible; the use case still pins its own authorized read.
type DraftInput struct {
	ID              string `json:"id"`
	ExpectedVersion *int64 `json:"expected_version,omitempty" minimum:"0"`
}

type DefinitionValidator interface {
	Validate(context.Context, model.Definition) model.Validation
}

type Definitions struct {
	Store    DefinitionRepository
	Identity *identity.Manager
	Engine   DefinitionValidator
	Mode     string
}

type documentKey struct{ kind, id string }
type revisions struct {
	documents map[documentKey]int64
	grants    map[string]int64
	checks    []func(*store.Tx) error
}

func newRevisions() *revisions { return &revisions{documents: map[documentKey]int64{}} }
func (v *revisions) remember(kind, id string, version int64) error {
	key := documentKey{kind, id}
	if prior, exists := v.documents[key]; exists && prior != version {
		return store.ErrConflict
	}
	v.documents[key] = version
	return nil
}
func (v *revisions) guards() []definitioncommit.Revision {
	guards := make([]definitioncommit.Revision, 0, len(v.documents)+1)
	for key, version := range v.documents {
		guards = append(guards, definitioncommit.Revision{Kind: key.kind, ID: key.id, Version: version})
	}
	if v.grants != nil {
		guards = append(guards, definitioncommit.Revision{Kind: "grant", Members: v.grants})
	}
	return guards
}
func (v *revisions) check(tx *store.Tx) error {
	if err := definitioncommit.CheckRevisions(tx, v.guards()); err != nil {
		return err
	}
	for _, check := range v.checks {
		if err := check(tx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Definitions) definition(ctx context.Context, id string, seen *revisions) (model.Definition, error) {
	doc, err := s.Store.Get(ctx, "definition", id)
	if errors.Is(err, store.ErrNotFound) {
		if rememberErr := seen.remember("definition", id, 0); rememberErr != nil {
			return model.Definition{}, rememberErr
		}
		return model.Definition{}, err
	}
	if err != nil {
		return model.Definition{}, err
	}
	if err := seen.remember("definition", id, doc.Version); err != nil {
		return model.Definition{}, err
	}
	return store.Decode[model.Definition](doc)
}

// access checks the definition, selectors, control resources and every transitive
// dependency. The returned revisions are checked in the mutation transaction.
func (s *Definitions) access(ctx context.Context, p identity.Principal, d model.Definition, action string, seen *revisions) error {
	if d.GroupID == "" {
		return identity.ErrDenied
	}
	if err := s.permit(ctx, p, action, d.GroupID, seen); err != nil {
		return err
	}
	if len(d.Selector.DeviceIDs) == 0 && d.Selector.AssetID == "" {
		return errors.New("select at least one device or an asset hierarchy")
	}
	resources := append([]string{}, d.Selector.DeviceIDs...)
	if d.Selector.AssetID != "" {
		resources = append(resources, d.Selector.AssetID)
	}
	for _, condition := range d.Policy.Conditions {
		resources = append(resources, condition.DeviceID)
	}
	for _, steps := range [][]model.Step{d.Policy.Steps, d.Policy.Degraded} {
		for _, step := range steps {
			resources = append(resources, step.DeviceID)
		}
	}
	for _, id := range resources {
		if id == "" {
			return identity.ErrDenied
		}
		if err := s.permit(ctx, p, "read", id, seen); err != nil {
			return err
		}
	}
	visiting := map[string]bool{d.ID: true}
	checked := map[string]bool{}
	var dependency func(string) error
	dependency = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("dependency cycle: %s", id)
		}
		if checked[id] {
			return nil
		}
		other, err := s.definition(ctx, id, seen)
		if err != nil {
			return err
		}
		if err = s.permit(ctx, p, "read", other.GroupID, seen); err != nil {
			return err
		}
		visiting[id] = true
		for _, nested := range other.Dependencies {
			if err := dependency(nested); err != nil {
				return err
			}
		}
		delete(visiting, id)
		checked[id] = true
		return nil
	}
	for _, id := range d.Dependencies {
		if err := dependency(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Definitions) loadDraft(ctx context.Context, p identity.Principal, input DraftInput, action string, seen *revisions) (model.Draft, error) {
	if err := s.captureAuthorization(ctx, p, seen); err != nil {
		return model.Draft{}, err
	}
	if err := s.permit(ctx, p, action, "", seen); err != nil {
		return model.Draft{}, err
	}
	if input.ExpectedVersion != nil && *input.ExpectedVersion < 0 {
		return model.Draft{}, errors.New("nonnegative versions are required")
	}
	doc, err := s.Store.Get(ctx, "draft", input.ID)
	if err != nil {
		return model.Draft{}, err
	}
	draft, err := store.Decode[model.Draft](doc)
	if err != nil {
		return model.Draft{}, err
	}
	if err = s.access(ctx, p, draft.Definition, action, seen); err != nil {
		return model.Draft{}, err
	}
	if input.ExpectedVersion != nil && doc.Version != *input.ExpectedVersion {
		return model.Draft{}, store.ErrConflict
	}
	seen.documents[documentKey{"draft", input.ID}] = doc.Version
	return draft, nil
}

// LoadDraft supplies the same resource checks to diff and simulation adapters.
func (s *Definitions) LoadDraft(ctx context.Context, p identity.Principal, input DraftInput, action string) (model.Draft, error) {
	return s.loadDraft(ctx, p, input, action, newRevisions())
}

func (s *Definitions) SaveDraft(ctx context.Context, p identity.Principal, input SaveDraftInput) (_ model.Draft, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "definitions.draft.save", observability.Identity{ActorID: p.Actor.UserID, DraftID: input.Draft.ID, DefinitionID: input.Draft.Definition.ID, DraftRevision: strconv.FormatInt(input.ExpectedVersion, 10)})
	defer func() { finish(operationErr) }()
	seen := newRevisions()
	if err := s.captureAuthorization(ctx, p, seen); err != nil {
		return model.Draft{}, err
	}
	if err := s.permit(ctx, p, "draft", "", seen); err != nil {
		return model.Draft{}, err
	}
	draft := input.Draft
	if input.ExpectedVersion < 0 || draft.BaseVersion < 0 {
		return model.Draft{}, errors.New("nonnegative versions are required")
	}
	if draft.ID == "" {
		draft.ID = draft.Definition.ID
	}
	if !model.ResourceID(draft.ID) || !model.ResourceID(draft.Definition.ID) {
		return model.Draft{}, errors.New("valid draft and definition IDs are required")
	}
	if err := s.access(ctx, p, draft.Definition, "draft", seen); err != nil {
		return model.Draft{}, err
	}
	if current, err := s.definition(ctx, draft.Definition.ID, seen); err == nil {
		if err = s.permit(ctx, p, "draft", current.GroupID, seen); err != nil {
			return model.Draft{}, err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return model.Draft{}, err
	}
	oldDoc, err := s.Store.Get(ctx, "draft", draft.ID)
	if err == nil {
		old, decodeErr := store.Decode[model.Draft](oldDoc)
		if decodeErr != nil {
			return model.Draft{}, decodeErr
		}
		if err = s.permit(ctx, p, "draft", old.Definition.GroupID, seen); err != nil {
			return model.Draft{}, err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return model.Draft{}, err
	}
	if oldDoc.Version != input.ExpectedVersion {
		return model.Draft{}, store.ErrConflict
	}
	seen.documents[documentKey{"draft", draft.ID}] = oldDoc.Version
	draft.AuthorID = p.Actor.UserID
	draft.Version = input.ExpectedVersion + 1
	draft.UpdatedMS = s.Store.CurrentTime().UnixMilli()
	draft.Definition.Status = "draft"
	draft.Definition.ExecutionPlan = nil
	ctx = observability.WithIdentity(ctx, observability.Identity{DraftID: draft.ID, DraftRevision: strconv.FormatInt(draft.Version, 10)})
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := seen.check(tx); err != nil {
			return err
		}
		if _, err := tx.Put("draft", draft.ID, input.ExpectedVersion, draft); err != nil {
			return err
		}
		return tx.Audit(p.Actor, "definition.draft.save", draft.Definition.ID, draft.ID, draft)
	})
	return draft, err
}

func (s *Definitions) ValidateDraft(ctx context.Context, p identity.Principal, input DraftInput) (_ model.Validation, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "definitions.draft.validate", observability.Identity{ActorID: p.Actor.UserID, DraftID: input.ID})
	defer func() { finish(operationErr) }()
	seen := newRevisions()
	draft, err := s.loadDraft(ctx, p, input, "draft", seen)
	if err != nil {
		return model.Validation{}, err
	}
	ctx = observability.WithIdentity(ctx, observability.Identity{DefinitionID: draft.Definition.ID, DraftRevision: strconv.FormatInt(draft.Version, 10)})
	validation := s.Engine.Validate(ctx, draft.Definition)
	// Record which revision was validated, including invalid graphs. Concurrent
	// edits return a conflict so the result cannot be mistaken for the new draft.
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := seen.check(tx); err != nil {
			return err
		}
		return tx.Audit(p.Actor, "definition.draft.validate", draft.Definition.ID, draft.ID, map[string]any{"draft_version": draft.Version, "validation": validation})
	})
	return validation, err
}

func (s *Definitions) PublishDraft(ctx context.Context, p identity.Principal, input DraftInput) (_ model.Definition, operationErr error) {
	ctx, finish := observability.StartOperation(ctx, "definitions.draft.publish", observability.Identity{ActorID: p.Actor.UserID, DraftID: input.ID})
	defer func() { finish(operationErr) }()
	if err := s.Identity.Permit(ctx, p, "publish", ""); err != nil {
		return model.Definition{}, err
	}
	if s.Mode == "edge" {
		return model.Definition{}, errors.New("new definitions are published in the cloud")
	}
	seen := newRevisions()
	draft, err := s.loadDraft(ctx, p, input, "publish", seen)
	if err != nil {
		return model.Definition{}, err
	}
	ctx = observability.WithIdentity(ctx, observability.Identity{DefinitionID: draft.Definition.ID, DraftRevision: strconv.FormatInt(draft.Version, 10)})
	current, err := s.definition(ctx, draft.Definition.ID, seen)
	if err == nil {
		if err = s.permit(ctx, p, "publish", current.GroupID, seen); err != nil {
			return model.Definition{}, err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return model.Definition{}, err
	}
	if draft.BaseVersion < 0 || seen.documents[documentKey{"definition", draft.Definition.ID}] != draft.BaseVersion {
		return model.Definition{}, store.ErrConflict
	}
	validation := s.Engine.Validate(ctx, draft.Definition)
	return definitioncommit.Commit(ctx, s.Store, p.Actor, definitioncommit.Prepared{
		Draft: draft, DraftVersion: seen.documents[documentKey{"draft", input.ID}], Validation: validation, Revisions: seen.guards(),
	})
}

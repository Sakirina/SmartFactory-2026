package application

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type definitionTestHarness struct {
	*Definitions
	Store *store.Store
}

func definitionsFixture(t *testing.T) (*definitionTestHarness, identity.Principal, model.Draft) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "application.db"), "cloud", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, entity := range []model.Entity{{ID: "factory", Kind: "asset"}, {ID: "other", Kind: "asset"}, {ID: "device", Kind: "device", ParentID: "factory"}, {ID: "forbidden", Kind: "device", ParentID: "other"}} {
		if _, err := db.Put(ctx, "entity", entity.ID, 0, entity); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Definitions{Store: db, Identity: &identity.Manager{Store: db}, Engine: &engine.Service{Store: db}, Mode: "cloud"}
	principal := identity.Principal{User: model.User{ID: "engineer", Active: true, Roles: []string{"engineer"}, Resources: []string{"factory"}}, Actor: model.Actor{UserID: "engineer", Source: "test"}}
	draft := model.Draft{ID: "working-copy", Definition: model.Definition{ID: "production-rule", Name: "Production rule", Kind: "analysis", SchemaVersion: model.ContractVersion, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "input", Type: "input"}}, Outputs: []model.Output{{NodeID: "input", Key: "reading", Type: "number"}}}}
	return &definitionTestHarness{Definitions: svc, Store: db}, principal, draft
}

func TestDefinitionUseCaseSavesValidatesPublishesWithAuditAndOutbox(t *testing.T) {
	s, p, draft := definitionsFixture(t)
	ctx := context.Background()
	saved, err := s.SaveDraft(ctx, p, SaveDraftInput{Draft: draft})
	if err != nil || saved.ID != draft.ID || saved.Version != 1 || saved.AuthorID != p.User.ID {
		t.Fatalf("save: %+v %v", saved, err)
	}
	revision := saved.Version
	input := DraftInput{ID: saved.ID, ExpectedVersion: &revision}
	validation, err := s.ValidateDraft(ctx, p, input)
	if err != nil || !validation.Valid {
		t.Fatalf("validate: %+v %v", validation, err)
	}
	published, err := s.PublishDraft(ctx, p, input)
	if err != nil || published.ID != draft.Definition.ID || published.Version != 1 || published.Status != "published" {
		t.Fatalf("publish: %+v %v", published, err)
	}
	doc, err := s.Store.Get(ctx, "draft", draft.ID)
	updated, decodeErr := store.Decode[model.Draft](doc)
	if err != nil || decodeErr != nil || updated.Version != 2 || updated.BaseVersion != 1 {
		t.Fatalf("stored draft: %+v %v %v", updated, err, decodeErr)
	}
	var outbox int
	if err := s.Store.DB.QueryRow("SELECT count(*) FROM outbox WHERE id IN ($1,$2)", "tb-definition:production-rule:1", "definition-sync:production-rule:1").Scan(&outbox); err != nil || outbox != 2 {
		t.Fatalf("publication deliveries: %d %v", outbox, err)
	}
	audit, err := s.Store.AuditList(ctx, draft.ID, 10)
	if err != nil || len(audit) != 3 {
		t.Fatalf("audit: %+v %v", audit, err)
	}
	for _, event := range audit {
		if event.Actor.UserID != p.User.ID || event.Resource != draft.Definition.ID || event.RequestID != draft.ID {
			t.Fatalf("lost actor or original IDs: %+v", event)
		}
	}
	if issues, err := s.Store.VerifyAudit(ctx); err != nil || len(issues) != 0 {
		t.Fatalf("audit verification: %+v %v", issues, err)
	}
	if _, err := s.PublishDraft(ctx, p, input); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale reviewed revision: %v", err)
	}
}

func TestDefinitionUseCaseChecksResourcesWithoutHTTP(t *testing.T) {
	for _, reference := range []string{"group", "selector", "asset", "condition", "step", "degraded", "dependency"} {
		t.Run(reference, func(t *testing.T) {
			s, p, draft := definitionsFixture(t)
			switch reference {
			case "group":
				draft.Definition.GroupID = "other"
			case "selector":
				draft.Definition.Selector.DeviceIDs = []string{"forbidden"}
			case "asset":
				draft.Definition.Selector.AssetID = "other"
			case "condition":
				draft.Definition.Policy.Conditions = []model.Condition{{DeviceID: "forbidden"}}
			case "step":
				draft.Definition.Policy.Steps = []model.Step{{DeviceID: "forbidden"}}
			case "degraded":
				draft.Definition.Policy.Degraded = []model.Step{{DeviceID: "forbidden"}}
			case "dependency":
				draft.Definition.Dependencies = []string{"private-rule"}
				_, err := s.Store.Put(context.Background(), "definition", "private-rule", 0, model.Definition{ID: "private-rule", GroupID: "other", Version: 1, Status: "published"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.SaveDraft(context.Background(), p, SaveDraftInput{Draft: draft}); !errors.Is(err, identity.ErrDenied) {
				t.Fatalf("resource authorization bypass: %v", err)
			}
			if _, err := s.Store.Get(context.Background(), "draft", draft.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("denied draft persisted: %v", err)
			}
		})
	}
}

func TestDefinitionUseCaseGuardsAIAndExistingOwnership(t *testing.T) {
	s, p, draft := definitionsFixture(t)
	ctx := context.Background()
	ai := p
	ai.User.AI = true
	ai.User.Roles = []string{"ai"}
	if _, err := s.SaveDraft(ctx, ai, SaveDraftInput{Draft: draft}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateDraft(ctx, ai, DraftInput{ID: draft.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishDraft(ctx, ai, DraftInput{ID: draft.ID}); !errors.Is(err, identity.ErrDenied) {
		t.Fatalf("AI publication: %v", err)
	}
	private := draft.Definition
	private.GroupID = "other"
	private.Version = 1
	if _, err := s.Store.Put(ctx, "definition", private.ID, 0, private); err != nil {
		t.Fatal(err)
	}
	draft.BaseVersion = 1
	if _, err := s.SaveDraft(ctx, p, SaveDraftInput{Draft: draft, ExpectedVersion: 1}); !errors.Is(err, identity.ErrDenied) {
		t.Fatalf("moving existing definition into permitted group: %v", err)
	}
	if _, err := s.PublishDraft(ctx, p, DraftInput{ID: draft.ID}); !errors.Is(err, identity.ErrDenied) {
		t.Fatalf("publishing over another resource: %v", err)
	}
}

type validatorFunc func(context.Context, model.Definition) model.Validation

func (f validatorFunc) Validate(ctx context.Context, definition model.Definition) model.Validation {
	return f(ctx, definition)
}

func TestPublicationDetectsChangesAfterAuthorization(t *testing.T) {
	for _, changed := range []string{"draft", "dependency", "transitive_dependency"} {
		t.Run(changed, func(t *testing.T) {
			s, p, draft := definitionsFixture(t)
			ctx := context.Background()
			dependency := draft.Definition
			dependency.ID = "dependency"
			dependency.Version = 1
			dependency.Status = "published"
			indirect := dependency
			indirect.ID = "indirect"
			if changed == "transitive_dependency" {
				if _, err := s.Store.Put(ctx, "definition", indirect.ID, 0, indirect); err != nil {
					t.Fatal(err)
				}
				dependency.Dependencies = []string{indirect.ID}
			}
			if _, err := s.Store.Put(ctx, "definition", dependency.ID, 0, dependency); err != nil {
				t.Fatal(err)
			}
			draft.Definition.Dependencies = []string{dependency.ID}
			saved, err := s.SaveDraft(ctx, p, SaveDraftInput{Draft: draft})
			if err != nil {
				t.Fatal(err)
			}
			validator := s.Engine
			s.Engine = validatorFunc(func(ctx context.Context, definition model.Definition) model.Validation {
				validation := validator.Validate(ctx, definition)
				if changed == "draft" {
					saved.Definition.GroupID = "other"
					_, err = s.Store.Put(ctx, "draft", saved.ID, saved.Version, saved)
				} else if changed == "dependency" {
					dependency.Status = "inactive"
					dependency.Version++
					_, err = s.Store.Put(ctx, "definition", dependency.ID, 1, dependency)
				} else {
					indirect.Version++
					indirect.Status = "inactive"
					_, err = s.Store.Put(ctx, "definition", indirect.ID, 1, indirect)
				}
				if err != nil {
					t.Fatal(err)
				}
				return validation
			})
			if _, err := s.PublishDraft(ctx, p, DraftInput{ID: draft.ID}); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("concurrent %s change: %v", changed, err)
			}
			if _, err := s.Store.Get(ctx, "definition", draft.Definition.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("stale publication persisted: %v", err)
			}
			var deliveries int
			if err := s.Store.DB.QueryRow("SELECT count(*) FROM outbox").Scan(&deliveries); err != nil || deliveries != 0 {
				t.Fatalf("stale publication delivery: %d %v", deliveries, err)
			}
		})
	}
}

func TestPublicationPinsAuthorizationInputs(t *testing.T) {
	for _, mutation := range []string{"resource-move", "ancestor-move", "grant-change", "grant-delete", "grant-create", "user-revoke"} {
		t.Run(mutation, func(t *testing.T) {
			s, p, draft := definitionsFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			p.User.Resources = nil
			p.User.Teams = []string{"operators"}
			p.User.Version = 1
			if _, err := s.Store.Put(ctx, "user", p.User.ID, 0, p.User); err != nil {
				t.Fatal(err)
			}
			grant := identity.Grant{ID: "scope", TeamID: "operators", GroupID: "factory", Resources: []string{"factory"}, Actions: []string{"read", "draft", "publish"}}
			if _, err := s.Store.Put(ctx, "grant", grant.ID, 0, grant); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SaveDraft(ctx, p, SaveDraftInput{Draft: draft}); err != nil {
				t.Fatal(err)
			}
			validator := s.Engine
			s.Engine = validatorFunc(func(ctx context.Context, definition model.Definition) model.Validation {
				validation := validator.Validate(ctx, definition)
				var err error
				switch mutation {
				case "resource-move", "ancestor-move":
					id := "device"
					if mutation == "ancestor-move" {
						id = "factory"
					}
					_, err = s.Store.Put(ctx, "entity", id, 1, model.Entity{ID: id, ParentID: "other", Version: 2})
				case "grant-change":
					grant.Actions = []string{"read"}
					_, err = s.Store.Put(ctx, "grant", grant.ID, 1, grant)
				case "grant-delete":
					_, err = s.Store.DB.ExecContext(ctx, "DELETE FROM documents WHERE kind='grant' AND id=$1", grant.ID)
				case "grant-create":
					grant.ID = "new-grant"
					_, err = s.Store.Put(ctx, "grant", grant.ID, 0, grant)
				case "user-revoke":
					user := p.User
					user.Active = false
					user.Version++
					_, err = s.Store.Put(ctx, "user", user.ID, 1, user)
				}
				if err != nil {
					t.Fatal(err)
				}
				return validation
			})
			if _, err := s.PublishDraft(ctx, p, DraftInput{ID: draft.ID}); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("authorization changed during publication: %s %v", mutation, err)
			}
			if _, err := s.Store.Get(ctx, "definition", draft.Definition.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("publication survived changed authorization: %v", err)
			}
		})
	}
}

func TestTransitiveDependenciesRequireEveryGroupPermission(t *testing.T) {
	s, p, draft := definitionsFixture(t)
	ctx := context.Background()
	indirect := draft.Definition
	indirect.ID, indirect.GroupID, indirect.Version, indirect.Status = "indirect", "other", 1, "published"
	direct := draft.Definition
	direct.ID, direct.Version, direct.Status = "direct", 1, "published"
	direct.Dependencies = []string{indirect.ID}
	for _, definition := range []model.Definition{direct, indirect} {
		if _, err := s.Store.Put(ctx, "definition", definition.ID, 0, definition); err != nil {
			t.Fatal(err)
		}
	}
	draft.Definition.Dependencies = []string{direct.ID}
	if _, err := s.SaveDraft(ctx, p, SaveDraftInput{Draft: draft}); !errors.Is(err, identity.ErrDenied) {
		t.Fatalf("indirect dependency authorization: %v", err)
	}
}

func TestPublicationRollsBackAllWritesWhenAuditFails(t *testing.T) {
	s, p, draft := definitionsFixture(t)
	ctx := context.Background()
	if _, err := s.SaveDraft(ctx, p, SaveDraftInput{Draft: draft}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB.Exec("CREATE TRIGGER reject_publish_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT, 'test audit failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishDraft(ctx, p, DraftInput{ID: draft.ID}); err == nil {
		t.Fatal("expected failed audit to abort publication")
	}
	for _, kind := range []string{"definition", "catalogue"} {
		docs, err := s.Store.List(ctx, kind)
		if err != nil || len(docs) != 0 {
			t.Fatalf("partial %s write: %+v %v", kind, docs, err)
		}
	}
	doc, err := s.Store.Get(ctx, "draft", draft.ID)
	if err != nil || doc.Version != 1 {
		t.Fatalf("draft changed despite rollback: %+v %v", doc, err)
	}
	var deliveries int
	if err := s.Store.DB.QueryRow("SELECT count(*) FROM outbox").Scan(&deliveries); err != nil || deliveries != 0 {
		t.Fatalf("partial delivery write: %d %v", deliveries, err)
	}
}

package application

import (
	"context"
	"errors"
	"testing"

	"competition2026/product/platform/internal/application/definitioncommit"
	"competition2026/product/platform/internal/compiledplan"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/pkg/model"
)

func TestPublicationPersistsVerifiablePlanAndRejectsMissingDependencyAtomically(t *testing.T) {
	t.Run("published_plan", func(t *testing.T) {
		s, principal, draft := definitionsFixture(t)
		ctx := context.Background()
		saved, err := s.SaveDraft(ctx, principal, SaveDraftInput{Draft: draft})
		if err != nil {
			t.Fatal(err)
		}
		published, err := s.PublishDraft(ctx, principal, DraftInput{ID: saved.ID, ExpectedVersion: &saved.Version})
		if err != nil || published.ExecutionPlan == nil {
			t.Fatal("publication omitted plan", published, err)
		}
		persisted, err := compiledplan.Load(ctx, s.Store, published)
		if err != nil || rulecore.Verify(persisted, published) != nil || persisted.ID != published.ExecutionPlan.ID || persisted.SHA256 != published.ExecutionPlan.SHA256 {
			t.Fatal("publication did not persist matching plan", persisted, err)
		}
	})
	t.Run("missing_dependency", func(t *testing.T) {
		s, principal, draft := definitionsFixture(t)
		ctx := context.Background()
		draft.Definition.Dependencies = []string{"unavailable"}
		draft.Version = 1
		if _, err := s.Store.Put(ctx, "draft", draft.ID, 0, draft); err != nil {
			t.Fatal(err)
		}
		_, err := definitioncommit.Commit(ctx, s.Store, principal.Actor, definitioncommit.Prepared{Draft: draft, DraftVersion: 1, Validation: model.Validation{Valid: true}})
		var invalid *compiledplan.DefinitionError
		if !errors.As(err, &invalid) || invalid.Code != "dependency_unavailable" {
			t.Fatal("publication accepted unavailable dependency", err)
		}
		for _, kind := range []string{"definition", "compiled_plan", "catalogue", "plan_issue"} {
			docs, err := s.Store.List(ctx, kind)
			if err != nil || len(docs) != 0 {
				t.Fatal("rejected publication left artifacts", kind, docs, err)
			}
		}
		document, err := s.Store.Get(ctx, "draft", draft.ID)
		if err != nil || document.Version != 1 {
			t.Fatal("rejected publication advanced draft", document, err)
		}
		var deliveries int
		if err := s.Store.DB.QueryRow("SELECT count(*) FROM outbox").Scan(&deliveries); err != nil || deliveries != 0 {
			t.Fatal("rejected publication scheduled delivery", deliveries, err)
		}
		audit, err := s.Store.AuditList(ctx, draft.ID, 10)
		if err != nil || len(audit) != 0 {
			t.Fatal("rejected publication committed audit", audit, err)
		}
	})
}

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func investigationApplication(t *testing.T) (*Investigations, *store.Store, identity.Principal, model.Definition) {
	t.Helper()
	f, p, draft := definitionsFixture(t)
	f.Identity.Master = make([]byte, 32)
	u := p.User
	u.Login = u.ID
	u.Name = "investigation-engineer"
	if _, err := f.Identity.CreateUser(context.Background(), p.Actor, u, "test-password-1234", "", 0); err != nil {
		t.Fatal(err)
	}
	_, p, err := f.Identity.Login(context.Background(), u.Login, "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	business := &Business{Store: f.Store, Identity: f.Identity, Definitions: f.Definitions, Mode: "cloud"}
	return &Investigations{Store: f.Store, Identity: f.Identity, Definitions: f.Definitions, Business: business}, f.Store, p, draft.Definition
}

func TestInvestigationWildcardResourceScope(t *testing.T) {
	s, _, p, _ := investigationApplication(t)
	ctx := context.Background()
	if err := s.checkResource(ctx, p, model.EvidenceResource{Kind: "resource", ID: "*"}); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("wildcard scope bypassed its permission", err)
	}
	p.User.Resources = []string{"*"}
	if err := s.checkResource(ctx, p, model.EvidenceResource{Kind: "resource", ID: "*"}); err != nil {
		t.Fatal("authorized wildcard scope treated as a missing entity", err)
	}
	if err := s.checkResource(ctx, p, model.EvidenceResource{Kind: "entity", ID: "*"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("concrete entity identity bypassed existence check", err)
	}
}

func TestInvestigationDocumentsAuthorizationPaginationAndOpaqueScope(t *testing.T) {
	s, db, p, d := investigationApplication(t)
	ctx := context.Background()
	for i, group := range []string{"factory", "other", "factory", "other", "factory"} {
		d.ID = fmt.Sprintf("定义/中文:%d", i)
		d.GroupID = group
		d.Version = 1
		if _, err := db.Put(ctx, "definition", d.ID, 0, d); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Documents(ctx, p, "definition", model.AIDocumentScope{Kind: "analysis", Limit: 1})
	if err != nil || len(first.Items) != 1 || !first.HasMore || first.NextAfter == "" {
		t.Fatal("first page", first, err)
	}
	var a model.Definition
	if err = store.DecodeJSON(first.Items[0], &a); err != nil || a.ID != "定义/中文:0" {
		t.Fatal("escaped identity missing", err)
	}
	second, err := s.Documents(ctx, p, "definition", model.AIDocumentScope{Kind: "analysis", Limit: 1, After: first.NextAfter})
	if err != nil || len(second.Items) != 1 {
		t.Fatal("filtered second page", second, err)
	}
	var b model.Definition
	_ = store.DecodeJSON(second.Items[0], &b)
	if b.ID != "定义/中文:2" {
		t.Fatal("unauthorized page member", b.ID)
	}
	third, err := s.Documents(ctx, p, "definition", model.AIDocumentScope{Kind: "analysis", Limit: 1, After: second.NextAfter})
	if err != nil || len(third.Items) != 1 || third.HasMore || third.NextAfter != "" {
		t.Fatal("final page", third, err)
	}
	if strings.Contains(first.NextAfter, "定义") || strings.Contains(first.NextAfter, "other") {
		t.Fatal("continuation exposes filtered candidate")
	}
	if _, err = s.Documents(ctx, p, "definition", model.AIDocumentScope{Kind: "alarm", Limit: 1, After: first.NextAfter}); !errors.Is(err, store.ErrQueryInvalid) {
		t.Fatal("filter change accepted", err)
	}
	if _, err = s.Documents(ctx, p, "draft", model.AIDocumentScope{Kind: "analysis", Limit: 1, After: first.NextAfter}); !errors.Is(err, store.ErrQueryInvalid) {
		t.Fatal("collection change accepted", err)
	}
	other := p.User
	other.ID = "other-user"
	other.Login = other.ID
	other.Version = 0
	if _, err = s.Identity.CreateUser(ctx, p.Actor, other, "test-password-1234", "", 0); err != nil {
		t.Fatal(err)
	}
	_, otherP, err := s.Identity.Login(ctx, other.Login, "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Documents(ctx, otherP, "definition", model.AIDocumentScope{Kind: "analysis", Limit: 1, After: first.NextAfter}); !errors.Is(err, store.ErrQueryInvalid) {
		t.Fatal("cross-user cursor accepted", err)
	}
}

func TestInvestigationDocumentsEmptyPageAndFiniteScanBudget(t *testing.T) {
	s, db, p, d := investigationApplication(t)
	ctx := context.Background()
	if err := db.Write(ctx, func(tx *store.Tx) error {
		for i := 0; i < 1005; i++ {
			d.ID = fmt.Sprintf("a-private-%04d", i)
			d.GroupID = "other"
			d.Version = 1
			if _, err := tx.Put("definition", d.ID, 0, d); err != nil {
				return err
			}
		}
		d.ID = "b-visible"
		d.GroupID = "factory"
		_, err := tx.Put("definition", d.ID, 0, d)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	page, err := s.Documents(ctx, p, "definition", model.AIDocumentScope{Kind: "analysis", Limit: 200})
	if err != nil || len(page.Items) != 0 || !page.HasMore || !page.BudgetExhausted || page.ScanBudget != 1000 || page.NextAfter == "" {
		t.Fatal("finite empty page", page, err)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "1005") || strings.Contains(string(encoded), "a-private") {
		t.Fatal("private candidate details exposed", string(encoded))
	}
	next, err := s.Documents(ctx, p, "definition", model.AIDocumentScope{Kind: "analysis", Limit: 200, After: page.NextAfter})
	if err != nil || len(next.Items) != 1 || next.HasMore || next.BudgetExhausted {
		t.Fatal("scan continuation", next, err)
	}
	for _, limit := range []int{-1, 201} {
		if _, err = s.Documents(ctx, p, "definition", model.AIDocumentScope{Limit: limit}); err == nil {
			t.Fatal("invalid scan limit accepted", limit)
		}
	}
}

func TestInvestigationEvidenceAuditRollbackAndCAS(t *testing.T) {
	s, db, p, _ := investigationApplication(t)
	ctx := context.Background()
	investigation, err := s.Begin(ctx, p, "openai", "responses", "fixture", []model.InvestigationMessage{{Role: "user", Content: "调查"}})
	if err != nil {
		t.Fatal(err)
	}
	e := model.InvestigationEvidence{ID: "evidence-test", InvestigationID: investigation.ID, ToolCallID: "actual-call", ToolName: "list_definitions", Status: "empty", VisibleContent: `{"items":[],"has_more":false}`, Resources: []model.EvidenceResource{}}
	if _, err = db.DB.ExecContext(ctx, `CREATE TRIGGER ai_audit_failure BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'fixture audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Record(ctx, p, e); err == nil {
		t.Fatal("audit failure committed evidence")
	}
	items, err := db.InvestigationEvidenceList(ctx, investigation.ID)
	if err != nil || len(items) != 0 {
		t.Fatal("rollback left evidence", items, err)
	}
	current, err := db.Investigation(ctx, investigation.ID)
	if err != nil || current.Version != 1 {
		t.Fatal("rollback advanced investigation", current, err)
	}
	if _, err = db.DB.ExecContext(ctx, "DROP TRIGGER ai_audit_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Record(ctx, p, e); err != nil {
		t.Fatal(err)
	}
	e.ID = "second-evidence"
	if _, err = s.Record(ctx, p, e); err == nil {
		t.Fatal("reused model ToolCallID accepted")
	}
	current.Version++
	if err = db.Write(ctx, func(tx *store.Tx) error { return tx.PutInvestigation(current, 1) }); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale investigation write accepted", err)
	}
	refs, status := s.References(ctx, p, investigation.ID, "引用 [evidence:evidence-test]")
	if status != "unsupported" || refs[0].Status != "not_delivered" {
		t.Fatal("prepared evidence accepted", refs, status)
	}
}

func TestInvestigationResourceExtractionUsesTypedFields(t *testing.T) {
	s, _, _, d := investigationApplication(t)
	d.Nodes[0].Params = map[string]any{"device_id": "text-private", "group_id": "text-group", "source_id": "text-source", "dependencies": []string{"text-dependency"}, "_resources": []model.EvidenceResource{{Kind: "entity", ID: "text-entity"}}}
	raw, _ := json.Marshal(d)
	var result map[string]any
	_ = store.DecodeJSON(raw, &result)
	result["_resources"] = []model.EvidenceResource{{Kind: "definition", ID: d.ID, Version: 3}, {Kind: "entity", ID: "device", Version: 2}, {Kind: "resource", ID: "factory", Version: 1}}
	resources := s.Resources("get_definition", map[string]any{"id": d.ID}, result)
	if len(resources) != 3 {
		t.Fatal("extension parameters changed authorization resources", resources)
	}
	for _, r := range resources {
		if strings.HasPrefix(r.ID, "text-") {
			t.Fatal("extension text became an access requirement", r)
		}
	}
	point := model.Observation{DeviceID: "device", SourceID: "source-node", EntityRevision: 2, Value: map[string]any{"device_id": "text-device", "group_id": "text-group", "dependencies": []string{"text-rule"}}}
	data, _ := json.Marshal(point)
	page := model.QueryPage{Items: []model.QueryRow{{Kind: "trend", ID: "point", Data: data}}, Scope: model.QueryRequest{ResourceIDs: []string{"device"}}}
	resources = s.Resources("query_data", nil, page)
	if len(resources) != 1 || resources[0].ID != "device" || resources[0].Version != 2 {
		t.Fatal("observation values changed access requirements", resources)
	}
}

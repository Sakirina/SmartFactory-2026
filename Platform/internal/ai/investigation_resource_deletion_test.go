package ai_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (h *evidenceHarness) hiddenEvidence(t *testing.T, investigation model.Investigation, state string) {
	t.Helper()
	status, raw := h.request(t, h.fixture.Token, "GET", "/api/sf/v1/investigations/"+investigation.ID, nil)
	var value model.Investigation
	if err := store.DecodeJSON(raw, &value); err != nil {
		t.Fatal(err)
	}
	if status != 200 || value.Answer != "" || len(value.Messages) != 0 || value.EvidenceStatus != "unsupported" || len(value.Evidence) != 1 || len(value.References) != 1 {
		t.Fatalf("investigation retains unavailable content: HTTP %d %s", status, raw)
	}
	e := value.Evidence[0]
	arguments := bytes.TrimSpace(e.Arguments)
	if bytes.Equal(arguments, []byte("null")) {
		arguments = nil
	}
	if e.AccessStatus != state || len(arguments) != 0 || e.VisibleContent != "" || len(e.Resources) != 0 || value.References[0].Status != state {
		t.Fatalf("invalid unavailable evidence state: %s", raw)
	}
	t.Logf("investigation_http=%d state=%s answer_bytes=%d messages=%d arguments_content_bytes=%d arguments_json=%s content_bytes=%d resources=%d", status, state, len(value.Answer), len(value.Messages), len(arguments), e.Arguments, len(e.VisibleContent), len(e.Resources))
}

func originalResourceDeletion(t *testing.T, postgres bool) {
	h := evidenceFixture(t, postgres, "responses", true)
	ctx := context.Background()
	h.resources(t, []string{"factory", "private-factory"})
	doc, err := h.server.Store.Get(ctx, "definition", h.plan.DefinitionID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.Decode[model.Definition](doc)
	if err != nil {
		t.Fatal(err)
	}
	d.GroupID = "private-factory"
	d.Version++
	if _, err = h.server.Store.Put(ctx, "definition", d.ID, doc.Version, d); err != nil {
		t.Fatal(err)
	}
	investigation, _ := h.investigate(t, "读取身份 "+d.ID)
	status, original := h.detail(t, investigation, investigation.Evidence[0])
	if status != 200 || original.AccessStatus != "valid" || investigation.EvidenceStatus != "supported" {
		t.Fatal("original authorized evidence unavailable", status, original)
	}
	found := false
	for _, r := range original.Resources {
		if r.Kind == "resource" && r.ID == "private-factory" && r.Version > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("original group identity or version missing", original.Resources)
	}
	d.GroupID = "factory"
	d.Version++
	if _, err = h.server.Store.Put(ctx, "definition", d.ID, doc.Version+1, d); err != nil {
		t.Fatal(err)
	}
	status, unchanged := h.detail(t, investigation, investigation.Evidence[0])
	if status != 200 || unchanged.VisibleContent != original.VisibleContent || unchanged.VisibleSHA256 != original.VisibleSHA256 || fmt.Sprint(unchanged.Resources) != fmt.Sprint(original.Resources) {
		t.Fatal("latest definition changed original evidence", status)
	}
	h.resources(t, []string{"factory"})
	status, _ = h.detail(t, investigation, investigation.Evidence[0])
	if status != 403 {
		t.Fatal("original resource revocation ignored", status)
	}
	h.hiddenEvidence(t, investigation, "forbidden")
	h.resources(t, []string{"factory", "private-factory"})
	status, restored := h.detail(t, investigation, investigation.Evidence[0])
	if status != 200 || restored.VisibleContent != original.VisibleContent || restored.VisibleSHA256 != original.VisibleSHA256 {
		t.Fatal("original evidence not restored", status)
	}
	if err = h.server.Store.Write(ctx, func(tx *store.Tx) error { return tx.Delete("entity", "private-factory") }); err != nil {
		t.Fatal(err)
	}
	status, after := h.detail(t, investigation, investigation.Evidence[0])
	if status != 404 || after.VisibleContent != "" {
		t.Fatal("deleted original resource remains accessible", status, after)
	}
	h.hiddenEvidence(t, investigation, "deleted")
	t.Logf("original_http=200 moved_definition_http=200 revoked_http=403 restored_http=200 deleted_http=%d original_bytes=%d original_sha256=%s original_resources=%v", status, original.VisibleBytes, original.VisibleSHA256, original.Resources)
}

func TestInvestigationSQLiteOriginalResourceDeletion(t *testing.T) {
	originalResourceDeletion(t, false)
}

func TestInvestigationPostgresOriginalResourceDeletion(t *testing.T) {
	originalResourceDeletion(t, true)
}

func TestInvestigationResourceKindsExistenceAndAuthorization(t *testing.T) {
	for _, kind := range []string{"definition", "draft", "execution", "analysis_run", "alarm", "catalogue", "entity", "resource"} {
		t.Run(kind, func(t *testing.T) {
			h := evidenceFixture(t, false, "responses", false)
			ctx := context.Background()
			id := map[string]string{"definition": h.plan.DefinitionID, "draft": h.plan.DefinitionID, "execution": h.plan.ExecutionID, "analysis_run": h.plan.RunID, "alarm": h.fixture.AlarmID, "catalogue": "目录/温度:原始", "entity": h.plan.DeviceID, "resource": "factory"}[kind]
			storedKind := kind
			if kind == "resource" {
				storedKind = "entity"
			}
			if kind == "catalogue" {
				if _, err := h.server.Store.Put(ctx, storedKind, id, 0, map[string]any{"id": id, "group_id": "factory"}); err != nil {
					t.Fatal(err)
				}
			}
			doc, err := h.server.Store.Get(ctx, storedKind, id)
			if err != nil {
				t.Fatal(err)
			}
			app := h.server.InvestigationApplication()
			p := h.principal(t)
			inv, err := app.Begin(ctx, p, "fixture", "resource-lifecycle", "resource-mapping-test", []model.InvestigationMessage{{Role: "user", Content: "核对原始资源"}})
			if err != nil {
				t.Fatal(err)
			}
			// This application-level matrix isolates each captured resource kind.
			// The tests above exercise the actual HTTP model/tool loop as well.
			e, err := app.Record(ctx, p, model.InvestigationEvidence{ID: "resource-kind-evidence", InvestigationID: inv.ID, ToolCallID: "resource-kind-call", ToolName: "resource-kind-test", Status: "ok", Arguments: []byte(`{"scope":"original"}`), VisibleContent: `{"original_resource":true}`, Resources: []model.EvidenceResource{{Kind: kind, ID: id, Version: doc.Version}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = app.Delivered(ctx, p, inv.ID, []string{e.ID}); err != nil {
				t.Fatal(err)
			}
			inv, err = app.Complete(ctx, p, inv.ID, "原始资源 [evidence:"+e.ID+"]")
			if err != nil || inv.EvidenceStatus != "supported" {
				t.Fatal("existing resource rejected", inv, err)
			}
			status, original := h.detail(t, inv, e)
			if status != 200 || original.AccessStatus != "valid" {
				t.Fatal("existing resource inaccessible", status)
			}
			h.resources(t, []string{"private-factory"})
			status, _ = h.detail(t, inv, e)
			if status != 403 {
				t.Fatal("revocation ignored", kind, status)
			}
			h.hiddenEvidence(t, inv, "forbidden")
			h.resources(t, []string{"factory"})
			status, restored := h.detail(t, inv, e)
			if status != 200 || restored.VisibleSHA256 != original.VisibleSHA256 {
				t.Fatal("restoration changed evidence", status)
			}
			if err = h.server.Store.Write(ctx, func(tx *store.Tx) error { return tx.Delete(storedKind, id) }); err != nil {
				t.Fatal(err)
			}
			status, _ = h.detail(t, inv, e)
			if status != 404 {
				t.Fatal("deleted resource accessible", kind, storedKind, status)
			}
			h.hiddenEvidence(t, inv, "deleted")
			t.Logf("kind=%s stored_kind=%s existing_http=200 revoked_http=403 restored_http=200 deleted_http=%d", kind, storedKind, status)
		})
	}
}

func TestInvestigationOriginalDependencyDeletion(t *testing.T) {
	h := evidenceFixture(t, false, "responses", true)
	ctx := context.Background()
	doc, err := h.server.Store.Get(ctx, "definition", h.plan.DefinitionID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.Decode[model.Definition](doc)
	if err != nil {
		t.Fatal(err)
	}
	dependency := d
	dependency.ID, dependency.Version = "依赖/原始:删除", 1
	if _, err = h.server.Store.Put(ctx, "definition", dependency.ID, 0, dependency); err != nil {
		t.Fatal(err)
	}
	d.Dependencies = []string{dependency.ID}
	d.Version++
	if _, err = h.server.Store.Put(ctx, "definition", d.ID, doc.Version, d); err != nil {
		t.Fatal(err)
	}
	inv, _ := h.investigate(t, "读取身份 "+d.ID)
	status, original := h.detail(t, inv, inv.Evidence[0])
	if status != 200 {
		t.Fatal(status)
	}
	found := false
	for _, r := range original.Resources {
		if r.Kind == "definition" && r.ID == dependency.ID && r.Version == 1 {
			found = true
		}
	}
	if !found {
		t.Fatal("original dependency missing", original.Resources)
	}
	d.Dependencies = nil
	d.Version++
	if _, err = h.server.Store.Put(ctx, "definition", d.ID, doc.Version+1, d); err != nil {
		t.Fatal(err)
	}
	if err = h.server.Store.Write(ctx, func(tx *store.Tx) error { return tx.Delete("definition", dependency.ID) }); err != nil {
		t.Fatal(err)
	}
	status, _ = h.detail(t, inv, inv.Evidence[0])
	if status != 404 {
		t.Fatal("removed original dependency accessible", status)
	}
	h.hiddenEvidence(t, inv, "deleted")
}

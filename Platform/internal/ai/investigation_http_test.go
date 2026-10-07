package ai_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/ai"
	"competition2026/product/platform/internal/aievidencefixture"
	"competition2026/product/platform/internal/api"
	"competition2026/product/platform/internal/businessfixture"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

type evidenceHarness struct {
	server  *api.Server
	fixture *businessfixture.Fixture
	plan    aievidencefixture.Plan
	model   *aievidencefixture.Model
	chat    *ai.Chat
	url     string
	mu      sync.Mutex
	visible map[string]string
	dsn     string
}

func evidenceFixture(t *testing.T, postgres bool, protocol string, stream bool) *evidenceHarness {
	t.Helper()
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "investigation.db")
	if postgres {
		dsn, _ = testdb.Postgres(t, "ai_evidence")
	}
	master := make([]byte, 32)
	db, err := store.Open(ctx, dsn, "edge-a", master)
	if err != nil {
		t.Fatal(err)
	}
	if postgres {
		db.DB.SetMaxOpenConns(3)
	} else {
		db.DB.SetMaxOpenConns(1)
	}
	db.DB.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	f, err := businessfixture.Seed(ctx, db, master, "test-password-1234")
	if err != nil {
		t.Fatal(err)
	}
	config := &configcenter.Service{Store: db, Identity: f.Identity}
	s := &api.Server{Store: db, Identity: f.Identity, Engine: f.Engine, Mode: "cloud", NodeID: "edge-a", Config: config, Control: &control.Service{Store: db, Identity: f.Identity, Definitions: f.Engine, NodeID: "edge-a"}}
	plan, err := aievidencefixture.Seed(ctx, s, f)
	if err != nil {
		t.Fatal(err)
	}
	h := &evidenceHarness{server: s, fixture: f, plan: plan, visible: map[string]string{}, dsn: dsn}
	m := &aievidencefixture.Model{Plan: plan, OnOutputs: func(outputs []ai.Message) {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, output := range outputs {
			h.visible[output.ToolCallID] = output.Content
		}
	}}
	h.model = m
	upstream := httptest.NewServer(m)
	t.Cleanup(upstream.Close)
	tools := &ai.Tools{API: s}
	chat := &ai.Chat{Tools: tools, Config: config, Investigations: s.InvestigationApplication(), Default: ai.ModelConfig{Provider: "openai", API: protocol, Stream: &stream, Endpoint: upstream.URL + "/v1", Model: "evidence-http-test", APIKey: "sk-fixture-private-api-key", TimeoutMS: 2000}}
	h.chat = chat
	s.Chat = chat
	s.MCP = &ai.MCP{Tools: tools}
	host := httptest.NewServer(observability.HTTPContext(s.Handler()))
	t.Cleanup(host.Close)
	h.url = host.URL
	return h
}

func (h *evidenceHarness) request(t *testing.T, token, method, path string, value any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if value != nil {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(method, h.url+path, body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}

func (h *evidenceHarness) investigate(t *testing.T, text string) (model.Investigation, string) {
	t.Helper()
	status, raw := h.request(t, h.fixture.Token, "POST", "/api/sf/v1/assistant", map[string]any{"messages": []ai.Message{{Role: "user", Content: text}}})
	if status != 200 {
		t.Fatalf("assistant status %d: %s", status, raw)
	}
	id := ""
	final := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := store.DecodeJSON([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if value, ok := event["investigation_id"].(string); ok {
			id = value
		}
		if event["type"] == "final" {
			final = true
		}
	}
	if id == "" {
		t.Fatal("investigation identity missing", string(raw))
	}
	status, detail := h.request(t, h.fixture.Token, "GET", "/api/sf/v1/investigations/"+id, nil)
	if status != 200 {
		t.Fatalf("investigation status %d: %s", status, detail)
	}
	var result model.Investigation
	if err := store.DecodeJSON(detail, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status == "completed" && !final {
		t.Fatal("completed investigation has no final event")
	}
	return result, string(raw)
}

func (h *evidenceHarness) detail(t *testing.T, investigation model.Investigation, evidence model.InvestigationEvidence) (int, model.InvestigationEvidence) {
	t.Helper()
	status, raw := h.request(t, h.fixture.Token, "GET", "/api/sf/v1/investigations/"+investigation.ID+"/evidence/"+evidence.ID, nil)
	var value model.InvestigationEvidence
	if status == 200 {
		if err := store.DecodeJSON(raw, &value); err != nil {
			t.Fatal(err)
		}
	}
	return status, value
}

func TestInvestigationActualHTTPProtocolAndVisibleEvidence(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", protocol, stream), func(t *testing.T) {
				h := evidenceFixture(t, false, protocol, stream)
				result, events := h.investigate(t, "调查温度、执行与历史依据")
				if result.Status != "completed" || result.EvidenceStatus != "supported" || len(result.Evidence) != 5 || len(result.References) != 5 {
					t.Fatalf("incomplete evidence: %+v\n%s", result, events)
				}
				proofs := []model.InvestigationEvidence{}
				for i, item := range result.Evidence {
					status, detail := h.detail(t, result, item)
					if status != 200 || detail.AccessStatus != "valid" || detail.Delivery != "delivered" || detail.ToolCallID != fmt.Sprintf("fixture-call-%d", i) {
						t.Fatalf("invalid evidence %d: %+v", status, detail)
					}
					h.mu.Lock()
					actual := h.visible[detail.ToolCallID]
					h.mu.Unlock()
					sum := sha256.Sum256([]byte(actual))
					if actual != detail.VisibleContent || hex.EncodeToString(sum[:]) != detail.VisibleSHA256 || len(actual) != detail.VisibleBytes || len(actual) > 524288 {
						t.Fatal("model-visible body differs from stored evidence")
					}
					proofs = append(proofs, detail)
					if detail.ToolName == "query_data" {
						if !strings.Contains(actual, "9007199254740993.125") || !strings.Contains(actual, "snapshot_cursor") || !strings.Contains(actual, "quality_scope") || !strings.Contains(actual, "has_more") {
							t.Fatal("trend evidence lost numeric or query metadata", actual)
						}
					}
					if detail.ToolName == "get_definition" && (len(detail.Resources) < 3 || detail.Resources[0].Version != 1) {
						t.Fatal("definition resource revisions missing", detail.Resources)
					}
				}
				if directory := os.Getenv("SF_AI_EVIDENCE_ARTIFACT_DIR"); directory != "" {
					raw, err := json.MarshalIndent(map[string]any{"api": protocol, "stream": stream, "investigation": result, "sse": events, "model_visible_evidence": proofs}, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(filepath.Join(directory, fmt.Sprintf("http-visible-%s-%v.json", protocol, stream)), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				status, _ := h.request(t, h.fixture.OtherToken, "GET", "/api/sf/v1/investigations/"+result.ID, nil)
				if status != 404 {
					t.Fatal("another user read investigation", status)
				}
				status, raw := h.request(t, h.fixture.OtherToken, "GET", "/api/sf/v1/investigations?limit=20", nil)
				if status != 200 || !strings.Contains(string(raw), `"items":[]`) {
					t.Fatal("owner filtering failed", status, string(raw))
				}
			})
		}
	}
}

func TestInvestigationCitationAndFailureStates(t *testing.T) {
	h := evidenceFixture(t, false, "responses", true)
	normal, _ := h.investigate(t, "调查温度、执行与历史依据")
	if normal.EvidenceStatus != "supported" {
		t.Fatal("normal evidence unavailable")
	}
	for _, tc := range []struct{ text, state, reference string }{{"伪造引用", "unsupported", "unknown"}, {"跨调查引用", "unsupported", "cross_investigation"}, {"空查询", "unsupported", "empty"}, {"查询失败", "unsupported", "tool_error"}, {"超预算", "unsupported", "budget_exceeded"}, {"无依据回答", "missing", ""}} {
		t.Run(tc.text, func(t *testing.T) {
			result, _ := h.investigate(t, tc.text)
			if result.EvidenceStatus != tc.state {
				t.Fatalf("state=%s want=%s", result.EvidenceStatus, tc.state)
			}
			if tc.reference != "" && (len(result.References) != 1 || result.References[0].Status != tc.reference) {
				t.Fatal("incorrect reference", result.References)
			}
			if tc.reference == "budget_exceeded" {
				_, detail := h.detail(t, result, result.Evidence[0])
				if detail.OriginalBytes <= 524288 || detail.VisibleBytes >= 1024 || len(detail.Resources) != 0 || !strings.Contains(detail.VisibleContent, "budget_exceeded") {
					t.Fatal("oversized original content entered evidence", detail)
				}
			}
		})
	}
	failed, events := h.investigate(t, "模拟中断")
	if failed.Status != "failed" || failed.EvidenceStatus != "unsupported" || strings.Contains(events, `"type":"done"`) || len(failed.Evidence) != 1 || failed.Evidence[0].Delivery != "prepared" || failed.Evidence[0].AccessStatus != "not_delivered" {
		t.Fatal("incomplete model response accepted as delivered", failed, events)
	}
}

func (h *evidenceHarness) principal(t *testing.T) identity.Principal {
	t.Helper()
	p, err := h.fixture.Identity.Authenticate(context.Background(), h.fixture.Token)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (h *evidenceHarness) resources(t *testing.T, resources []string) {
	t.Helper()
	p := h.principal(t)
	u := p.User
	u.Resources = resources
	if _, err := h.fixture.Identity.CreateUser(context.Background(), p.Actor, u, "", "", u.Version); err != nil {
		t.Fatal(err)
	}
}

func TestInvestigationOriginalResourcesSurviveDefinitionEdits(t *testing.T) {
	h := evidenceFixture(t, false, "responses", true)
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
	dependency := d
	dependency.ID = "依赖/私有:一号"
	dependency.GroupID = "private-factory"
	dependency.Selector.DeviceIDs = []string{"private-device"}
	dependency.Version = 1
	if _, err = h.server.Store.Put(ctx, "definition", dependency.ID, 0, dependency); err != nil {
		t.Fatal(err)
	}
	d.Dependencies = []string{dependency.ID}
	d.Selector.DeviceIDs = append(d.Selector.DeviceIDs, "private-device")
	d.Version++
	if _, err = h.server.Store.Put(ctx, "definition", d.ID, doc.Version, d); err != nil {
		t.Fatal(err)
	}
	result, _ := h.investigate(t, "读取身份 "+d.ID)
	if result.EvidenceStatus != "supported" {
		t.Fatal("original authorized evidence failed", result)
	}
	_, original := h.detail(t, result, result.Evidence[0])
	found := false
	for _, r := range original.Resources {
		if r.Kind == "definition" && r.ID == dependency.ID && r.Version == 1 {
			found = true
		}
	}
	if !found || !strings.Contains(original.VisibleContent, "private-device") {
		t.Fatal("original dependency or selectors omitted", original.Resources)
	}
	d.Dependencies = nil
	d.Selector.DeviceIDs = []string{h.plan.DeviceID}
	d.Version++
	if _, err = h.server.Store.Put(ctx, "definition", d.ID, doc.Version+1, d); err != nil {
		t.Fatal(err)
	}
	dependency.GroupID = "factory"
	dependency.Selector.DeviceIDs = []string{h.plan.DeviceID}
	dependency.Version++
	if _, err = h.server.Store.Put(ctx, "definition", dependency.ID, 1, dependency); err != nil {
		t.Fatal(err)
	}
	h.resources(t, []string{"factory"})
	status, _ := h.detail(t, result, result.Evidence[0])
	if status != 403 {
		t.Fatal("old private resources disappeared with latest definition", status)
	}
	status, raw := h.request(t, h.fixture.Token, "GET", "/api/sf/v1/investigations/"+result.ID, nil)
	var hidden model.Investigation
	_ = store.DecodeJSON(raw, &hidden)
	if status != 200 || hidden.Answer != "" || len(hidden.Messages) != 0 || len(hidden.Evidence[0].Resources) != 0 || hidden.References[0].Status != "forbidden" {
		t.Fatal("revoked evidence content remained visible", string(raw))
	}
	h.resources(t, []string{"factory", "private-factory"})
	status, restored := h.detail(t, result, result.Evidence[0])
	if status != 200 || restored.VisibleContent != original.VisibleContent || restored.VisibleSHA256 != original.VisibleSHA256 {
		t.Fatal("stored old version changed after permission restoration")
	}
}

func evidenceExpiryDeletion(t *testing.T, postgres bool) {
	h := evidenceFixture(t, postgres, "responses", false)
	ctx := context.Background()
	result, _ := h.investigate(t, "读取身份 "+h.plan.DefinitionID)
	status, original := h.detail(t, result, result.Evidence[0])
	if status != 200 {
		t.Fatal(status)
	}
	h.resources(t, []string{"private-factory"})
	status, _ = h.detail(t, result, result.Evidence[0])
	if status != 403 {
		t.Fatal("revocation not enforced", status)
	}
	h.resources(t, []string{"factory"})
	status, restored := h.detail(t, result, result.Evidence[0])
	if status != 200 || restored.VisibleContent != original.VisibleContent {
		t.Fatal("restoration lost original evidence")
	}
	expired := original
	expired.ExpiresMS = time.Now().Add(-time.Minute).UnixMilli()
	if err := h.server.Store.Write(ctx, func(tx *store.Tx) error { return tx.PutInvestigationEvidence(expired, false) }); err != nil {
		t.Fatal(err)
	}
	status, _ = h.detail(t, result, result.Evidence[0])
	if status != 410 {
		t.Fatal("expired evidence readable", status)
	}
	deleted, _ := h.investigate(t, "读取身份 "+h.plan.DefinitionID)
	status, _ = h.request(t, h.fixture.Token, "DELETE", "/api/sf/v1/investigations/"+deleted.ID, nil)
	if status != 200 {
		t.Fatal(status)
	}
	status, _ = h.detail(t, deleted, deleted.Evidence[0])
	if status != 404 {
		t.Fatal("deleted evidence readable", status)
	}
	missing, _ := h.investigate(t, "读取身份 "+h.plan.DefinitionID)
	if err := h.server.Store.Write(ctx, func(tx *store.Tx) error { return tx.Delete("definition", h.plan.DefinitionID) }); err != nil {
		t.Fatal(err)
	}
	status, _ = h.detail(t, missing, missing.Evidence[0])
	if status != 404 {
		t.Fatal("deleted original resource remained accessible", status)
	}
}
func TestInvestigationSQLiteRevocationExpiryAndDeletion(t *testing.T) {
	evidenceExpiryDeletion(t, false)
}
func TestInvestigationPostgresRevocationExpiryAndDeletion(t *testing.T) {
	evidenceExpiryDeletion(t, true)
}

func TestInvestigationEscapedDraftIdentityAndSecretFiltering(t *testing.T) {
	h := evidenceFixture(t, false, "chat_completions", false)
	ctx := context.Background()
	doc, _ := h.server.Store.Get(ctx, "definition", h.plan.DefinitionID)
	d, _ := store.Decode[model.Definition](doc)
	d.ID = "草稿/中文:一号"
	d.Version = 0
	d.Status = "draft"
	d.Nodes[0].Params = map[string]any{"api_key": "sk-fixture-private-api-key", "password": "fixture-password", "authorization": "Bearer fixture-authorization-token", "token": "fixture-token"}
	r := httptest.NewRequest("POST", "/mcp/draft", nil)
	r.Header.Set("Authorization", "Bearer "+h.fixture.Token)
	tools := &ai.Tools{API: h.server}
	value, err := tools.Call(r, "save_draft", map[string]any{"draft": model.Draft{ID: d.ID, Definition: d}, "expected_version": 0}, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	var draft model.Draft
	_ = store.DecodeJSON(raw, &draft)
	for _, name := range []string{"validate_draft", "diff_draft", "simulate_draft"} {
		args := map[string]any{"id": draft.ID}
		if name == "simulate_draft" {
			args["point"] = model.Observation{DeviceID: h.plan.DeviceID, Key: "temperature", Value: 35, ObservedMS: time.Now().UnixMilli(), Quality: "GOOD"}
		}
		if _, err = tools.Call(r, name, args, true); err != nil {
			t.Fatalf("escaped %s failed: %v", name, err)
		}
	}
	if _, err = tools.Call(r, "get_definition", map[string]any{"id": h.plan.DefinitionID}, true); err != nil {
		t.Fatal(err)
	}
	d.ID = "凭据/过滤:一号"
	d.Status = "published"
	d.Version = 1
	if _, err = h.server.Store.Put(ctx, "definition", d.ID, 0, d); err != nil {
		t.Fatal(err)
	}
	result, _ := h.investigate(t, "读取身份 "+d.ID)
	_, detail := h.detail(t, result, result.Evidence[0])
	if strings.Contains(detail.VisibleContent, "fixture-password") || strings.Contains(detail.VisibleContent, "fixture-authorization-token") || strings.Contains(detail.VisibleContent, "fixture-token") || strings.Contains(detail.VisibleContent, "sk-fixture-private-api-key") || !strings.Contains(detail.VisibleContent, "[redacted]") {
		t.Fatal("secrets persisted in evidence", detail.VisibleContent)
	}
	status, _ := h.request(t, h.fixture.Token, "GET", "/api/sf/v1/definitions/"+url.PathEscape(h.plan.DefinitionID)+"/versions", nil)
	if status != 200 {
		t.Fatal("escaped resource navigation failed", status)
	}
}

func TestInvestigationDirectDevicePermissionUsesQueryAuthorization(t *testing.T) {
	h := evidenceFixture(t, false, "responses", false)
	h.resources(t, []string{h.plan.DeviceID})
	status, body := h.request(t, h.fixture.Token, "POST", "/api/sf/v1/queries/trend", model.QueryRequest{ResourceIDs: []string{h.plan.DeviceID}, Keys: []string{"temperature"}, FromMS: h.plan.FromMS, ToMS: h.plan.ToMS, Limit: 20})
	var page model.QueryPage
	if err := store.DecodeJSON(body, &page); err != nil || status != 200 || len(page.Items) == 0 {
		t.Fatalf("formal device query failed: %d %s", status, body)
	}
	if err := h.fixture.Identity.Permit(context.Background(), h.principal(t), "read", h.server.NodeID); err == nil {
		t.Fatal("fixture accidentally permits source node")
	}
	result, _ := h.investigate(t, "仅查询温度")
	if result.EvidenceStatus != "supported" || len(result.References) != 1 || result.References[0].Status != "valid" {
		t.Fatalf("direct device query evidence=%s references=%v", result.EvidenceStatus, result.References)
	}
	status, original := h.detail(t, result, result.Evidence[0])
	if status != 200 || !strings.Contains(original.VisibleContent, `"source_id":"edge-a"`) {
		t.Fatal("source provenance omitted", status)
	}
	for _, r := range original.Resources {
		if r.ID == h.server.NodeID {
			t.Fatal("source provenance became an access requirement", r)
		}
	}
	h.resources(t, []string{"private-device"})
	status, _ = h.detail(t, result, result.Evidence[0])
	if status != 403 {
		t.Fatal("original device revocation ignored", status)
	}
}

func investigationRestart(t *testing.T, postgres bool) {
	h := evidenceFixture(t, postgres, "responses", true)
	result, _ := h.investigate(t, "调查温度、执行与历史依据")
	if result.EvidenceStatus != "supported" || len(result.Evidence) != 5 {
		t.Fatal("full persisted tool loop failed", result)
	}
	_, original := h.detail(t, result, result.Evidence[0])
	if err := h.server.Store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), h.dsn, "edge-a", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if postgres {
		db.DB.SetMaxOpenConns(3)
	} else {
		db.DB.SetMaxOpenConns(1)
	}
	db.DB.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	h.server.Store = db
	h.fixture.Store = db
	h.fixture.Identity.Store = db
	h.server.Engine.Store = db
	h.server.Control.Store = db
	h.server.Config.Store = db
	status, restored := h.detail(t, result, result.Evidence[0])
	if status != 200 || restored.VisibleContent != original.VisibleContent || restored.VisibleSHA256 != original.VisibleSHA256 || restored.ToolCallID != original.ToolCallID {
		t.Fatal("restart changed original model result", status)
	}
	status, raw := h.request(t, h.fixture.Token, "GET", "/api/sf/v1/investigations/"+result.ID, nil)
	var saved model.Investigation
	_ = store.DecodeJSON(raw, &saved)
	if status != 200 || saved.Status != "completed" || saved.EvidenceStatus != "supported" || len(saved.Evidence) != 5 || saved.Answer != result.Answer {
		t.Fatal("investigation did not survive restart", status, string(raw))
	}
}
func TestInvestigationSQLiteRestartPreservesHTTPResults(t *testing.T) { investigationRestart(t, false) }
func TestInvestigationPostgresRestartPreservesHTTPResults(t *testing.T) {
	investigationRestart(t, true)
}

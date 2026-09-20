package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func scopedServer(t *testing.T, ai bool) (*Server, string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "api.db"), "cloud", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m := &identity.Manager{Store: s, Master: make([]byte, 32)}
	for _, e := range []model.Entity{{ID: "a", Kind: "asset", Version: 1}, {ID: "b", Kind: "asset", Version: 1}, {ID: "device-a", Kind: "device", ParentID: "a", EdgeID: "edge-a", Version: 1}, {ID: "device-b", Kind: "device", ParentID: "b", EdgeID: "edge-b", Version: 1}, {ID: "edge-a", Kind: "edge", ParentID: "a", Version: 1}, {ID: "edge-b", Kind: "edge", ParentID: "b", Version: 1}} {
		if _, err = s.Put(ctx, "entity", e.ID, 0, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = m.CreateUser(ctx, model.Actor{}, model.User{ID: "user", Name: "Scoped", Login: "user", Active: true, Roles: []string{"engineer"}, Resources: []string{"a"}, AI: ai}, "test-password-1234", "", 0); err != nil {
		t.Fatal(err)
	}
	token, _, err := m.Login(ctx, "user", "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Store: s, Identity: m, Engine: &engine.Service{Store: s}, Mode: "cloud"}, token
}
func call(s *Server, token, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestDraftAndQueriesEnforceEveryReferencedResource(t *testing.T) {
	s, token := scopedServer(t, false)
	d := model.Definition{ID: "test", Name: "Test", Kind: "analysis", GroupID: "a", SchemaVersion: "1.0", Selector: model.Selector{DeviceIDs: []string{"device-b"}, Keys: []string{"value"}}, Nodes: []model.Node{{ID: "input", Type: "input"}}}
	for _, ref := range []string{"selector", "condition", "step", "degraded"} {
		t.Run(ref, func(t *testing.T) {
			d.Selector.DeviceIDs = []string{"device-a"}
			d.Policy = model.Policy{}
			switch ref {
			case "selector":
				d.Selector.DeviceIDs = []string{"device-b"}
			case "condition":
				d.Policy.Conditions = []model.Condition{{DeviceID: "device-b"}}
			case "step":
				d.Policy.Steps = []model.Step{{DeviceID: "device-b"}}
			case "degraded":
				d.Policy.Degraded = []model.Step{{DeviceID: "device-b"}}
			}
			w := call(s, token, "POST", "/api/sf/v1/drafts", map[string]any{"draft": model.Draft{ID: d.ID, Definition: d}, "expected_version": 0})
			if w.Code != 403 {
				t.Fatalf("cross-resource draft: %d %s", w.Code, w.Body.String())
			}
		})
	}
	if w := call(s, token, "GET", "/api/sf/v1/data?device_ids=device-b", nil); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	d.Policy = model.Policy{}
	d.Selector.DeviceIDs = []string{"device-a"}
	if w := call(s, token, "POST", "/api/sf/v1/drafts", map[string]any{"draft": model.Draft{ID: d.ID, Definition: d}, "expected_version": 0}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := s.Store.Write(context.Background(), func(tx *store.Tx) error { return tx.SetEphemeral("source", "edge-b", model.SourceState{ID: "edge-b"}) }); err != nil {
		t.Fatal(err)
	}
	w := call(s, token, "GET", "/api/sf/v1/data?device_ids=device-a", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "edge-b") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestAIIdentityCannotInvokePrivilegedRoutes(t *testing.T) {
	s, token := scopedServer(t, true)
	for _, target := range []struct{ method, path string }{{"POST", "/api/sf/v1/drafts/test/publish"}, {"POST", "/api/sf/v1/executions"}, {"POST", "/api/sf/v1/executions/test/approve"}, {"POST", "/api/sf/v1/executions/test/dispatch"}, {"GET", "/api/sf/v1/config"}, {"POST", "/api/sf/v1/config"}, {"POST", "/api/sf/v1/ingest"}, {"POST", "/api/sf/v1/users"}, {"GET", "/api/sf/v1/audit"}} {
		w := call(s, token, target.method, target.path, map[string]any{})
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s: %d %s", target.method, target.path, w.Code, w.Body.String())
		}
	}
}

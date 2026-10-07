package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReleaseHTTPRequiredFieldsAndRevokedSession(t *testing.T) {
	s, f := businessServer(t)
	for _, item := range []struct {
		path, missing string
		body          map[string]any
	}{
		{"/releases", "request_id", map[string]any{"manifest": map[string]any{}}},
		{"/release-deployments", "request_id", map[string]any{"id": "d", "release_id": "r", "group_id": "factory", "batches": [][]string{{"w"}}, "reason": "review"}},
		{"/release-deployments/d/actions", "expected_version", map[string]any{"request_id": "pause", "action": "pause", "reason": "review"}},
		{"/release-deployments/d/rollback", "expected_version", map[string]any{"request_id": "rollback", "id": "old", "release_id": "r", "reason": "review"}},
		{"/template-batches/b/evolve", "expected_version", map[string]any{"id": "e", "request_id": "e", "release_id": "r", "name": "evolution", "program_sha256": strings.Repeat("a", 64), "targets": []any{map[string]any{"instance_id": "i", "template_version": 2}}}},
	} {
		response := call(s, f.Token, "POST", "/api/sf/v1"+item.path, item.body)
		if response.Code != 422 || !strings.Contains(response.Body.String(), item.missing) {
			t.Fatalf("%s: missing %s was not required: %d %s", item.path, item.missing, response.Code, response.Body.String())
		}
	}
	if response := call(s, f.Token, "GET", "/api/sf/v1/releases", nil); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if err := f.Identity.Logout(context.Background(), f.Token); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/releases", "/release-artifacts", "/release-deployments"} {
		response := call(s, f.Token, "GET", "/api/sf/v1"+path, nil)
		if response.Code != 401 {
			t.Fatal("revoked session", path, response.Code, response.Body.String())
		}
	}
}

func TestReleaseHTTPRejectsUserAndSharedServiceTokenAsWorkload(t *testing.T) {
	s, f := businessServer(t)
	for _, token := range []string{f.Token, s.ServiceToken} {
		req := httptest.NewRequest("GET", "/internal/releases/desired", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-SF-Instance-ID", "forged-instance")
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, req)
		if response.Code != 401 {
			t.Fatal("non-workload token accepted", response.Code, response.Body.String())
		}
	}
}

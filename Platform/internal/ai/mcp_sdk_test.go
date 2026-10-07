package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/app"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/compatibility"
	"competition2026/product/platform/pkg/model"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(copy)
}
func TestOfficialMCPSDKProtocolsAndAuthorization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	application, err := app.Open(ctx, app.Options{Mode: "cloud", NodeID: "mcp-test", DSN: filepath.Join(dir, "db"), KeyFile: filepath.Join(dir, "key"), BootstrapPassword: "test-password-1234", Seed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	host := httptest.NewServer(application.Server.Handler())
	defer host.Close()
	for _, version := range mcp.SupportedProtocolVersions() {
		t.Run(version, func(t *testing.T) {
			token, _, err := application.Server.Identity.Login(ctx, "assistant", "test-password-1234", "", false, "test")
			if err != nil {
				t.Fatal(err)
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "smartfactory-acceptance", Version: "1"}, nil)
			transport := &mcp.StreamableClientTransport{Endpoint: host.URL + "/mcp/draft", HTTPClient: &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}}, DisableStandaloneSSE: true, MaxRetries: -1}
			session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: version})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if got := session.InitializeResult().ProtocolVersion; got != version {
				t.Fatalf("negotiated %s, want %s", got, version)
			}
			list, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, tool := range list.Tools {
				names[tool.Name] = true
			}
			if !names["save_draft"] || names["publish_definition"] || names["control_device"] {
				t.Fatal(names)
			}
			catalogue, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "discover_catalogue", Arguments: map[string]any{}})
			if err != nil || catalogue.IsError || catalogue.StructuredContent == nil {
				t.Fatal(catalogue, err)
			}
			definition := app.ExampleDefinitions()[2]
			definition.Nodes[1].Params["value"] = json.Number("9223372036854775807")
			definition.ID = "sdk-" + strings.ReplaceAll(version, "-", "")
			definition.Name = "SDK draft"
			definition.Version = 0
			definition.GroupID = "factory"
			definition.SchemaVersion = "1.0"
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "save_draft", Arguments: map[string]any{"draft": model.Draft{ID: definition.ID, Definition: definition}, "expected_version": 0}})
			if err != nil || result.IsError {
				encoded, _ := json.Marshal(result)
				t.Fatal(string(encoded), err)
			}
			document, err := application.Store.Get(ctx, "draft", definition.ID)
			if err != nil || document.Version != 1 {
				t.Fatal(document, err)
			}
			if !strings.Contains(string(document.Data), `"value":9223372036854775807`) {
				t.Fatal("MCP rounded a draft integer")
			}
			_, err = application.Store.Get(ctx, "definition", definition.ID)
			if err != store.ErrNotFound {
				t.Fatal("MCP published a draft", err)
			}
			result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "save_draft", Arguments: map[string]any{"draft": model.Draft{ID: definition.ID, Definition: definition}, "expected_version": 0}})
			if err != nil || !result.IsError {
				t.Fatal("conflicting write accepted", result, err)
			}
			if err = application.Server.Identity.Logout(ctx, token); err != nil {
				t.Fatal(err)
			}
			if _, err = session.ListTools(ctx, nil); err == nil {
				t.Fatal("revoked identity reused MCP access")
			}
		})
	}
}

func TestMCPSDKRechecksResourcePermissionsWithExistingClient(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	application, err := app.Open(ctx, app.Options{Mode: "cloud", NodeID: "mcp-revoke", DSN: filepath.Join(dir, "db"), KeyFile: filepath.Join(dir, "key"), BootstrapPassword: "test-password-1234", Seed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	host := httptest.NewServer(application.Server.Handler())
	defer host.Close()
	token, _, err := application.Server.Identity.Login(ctx, "assistant", "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "revocation-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: host.URL + "/mcp/read", HTTPClient: &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	args := map[string]any{"device_ids": "climate-1", "keys": "temperature", "from_ms": 1, "to_ms": 2}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "query_data", Arguments: args})
	if err != nil || result.IsError {
		t.Fatal("permitted query failed", err)
	}
	doc, err := application.Store.Get(ctx, "user", "assistant")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.Decode[model.User](doc)
	if err != nil {
		t.Fatal(err)
	}
	user.Resources = []string{"counter-1"}
	user, err = application.Server.Identity.CreateUser(ctx, model.Actor{UserID: "admin"}, user, "", "", doc.Version)
	if err != nil {
		t.Fatal(err)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "query_data", Arguments: args})
	if err != nil || !result.IsError {
		t.Fatal("removed resource remained available", err)
	}
	user.Active = false
	if _, err = application.Server.Identity.CreateUser(ctx, model.Actor{UserID: "admin"}, user, "", "", user.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = session.ListTools(ctx, nil); err == nil {
		t.Fatal("disabled identity retained MCP permission")
	}
}

func TestMCPCompatibilityMatchesOfficialSDK(t *testing.T) {
	protocol := compatibility.Current().Protocols["mcp"]
	versions := mcp.SupportedProtocolVersions()
	if len(versions) == 0 || protocol.Current != versions[0] || !slices.Equal(protocol.Read, versions) || !slices.Equal(protocol.Write, versions) {
		t.Fatalf("MCP manifest differs from SDK: %+v supported=%v", protocol, versions)
	}
	apis := compatibility.Current().Protocols["model_api"]
	if apis.Current != "responses" || !slices.Equal(apis.Read, []string{"responses", "chat_completions"}) || !slices.Equal(apis.Write, apis.Read) {
		t.Fatalf("model API manifest differs from adapters: %+v", apis)
	}
}

func TestMCPSDKRequestValidationAndReadOnlyTools(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	application, err := app.Open(ctx, app.Options{Mode: "cloud", NodeID: "mcp-errors", DSN: filepath.Join(dir, "db"), KeyFile: filepath.Join(dir, "key"), BootstrapPassword: "test-password-1234", Seed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	aiToken, _, err := application.Server.Identity.Login(ctx, "assistant", "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	humanToken, _, err := application.Server.Identity.Login(ctx, "admin", "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	handler := application.Server.Handler()
	for _, sample := range []struct {
		name, token, method, path, origin, version, body string
		status                                           int
	}{
		{"human identity", humanToken, "POST", "/mcp/read", "", "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, 403},
		{"foreign origin", aiToken, "POST", "/mcp/read", "https://untrusted.invalid", "", `{}`, 403},
		{"unsupported version", aiToken, "POST", "/mcp/read", "", "1900-01-01", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, 400},
		{"invalid JSON", aiToken, "POST", "/mcp/read", "", "", `{`, 400},
		{"stateless GET", aiToken, "GET", "/mcp/read", "", "", "", 405},
		{"stateless DELETE", aiToken, "DELETE", "/mcp/read", "", "", "", 405},
		{"missing identity", "", "POST", "/mcp/read", "", "", `{}`, 401},
		{"notification", aiToken, "POST", "/mcp/read", "", "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, 202},
	} {
		t.Run(sample.name, func(t *testing.T) {
			request := httptest.NewRequest(sample.method, sample.path, strings.NewReader(sample.body))
			request.Header.Set("Authorization", "Bearer "+sample.token)
			request.Header.Set("Origin", sample.origin)
			request.Header.Set("MCP-Protocol-Version", sample.version)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != sample.status {
				t.Fatal(response.Code, response.Body.String())
			}
			if response.Header().Get("Mcp-Session-Id") != "" {
				t.Fatal("server created persistent protocol session")
			}
		})
	}
	for _, sample := range []struct {
		name, accept, contentType, body string
		status                          int
	}{
		{"unsupported content type", "application/json, text/event-stream", "text/plain", `{}`, 415},
		{"incomplete accept", "application/json", "application/json", `{}`, 400},
		{"request budget", "application/json, text/event-stream", "application/json", `{"unused":"` + strings.Repeat("x", 2<<20) + `"}`, 413},
	} {
		t.Run(sample.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/mcp/read", strings.NewReader(sample.body))
			request.Header.Set("Authorization", "Bearer "+aiToken)
			request.Header.Set("Accept", sample.accept)
			request.Header.Set("Content-Type", sample.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != sample.status {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	request := httptest.NewRequest("POST", "/mcp/read", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+aiToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || strings.Contains(response.Body.String(), `"name":"save_draft"`) {
		t.Fatal(response.Code, response.Body.String())
	}
}

package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"competition2026/product/platform/internal/ai"
	"competition2026/product/platform/internal/aimock"
	"competition2026/product/platform/internal/app"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestInterleavedToolArgumentStreamingAndInterruptedResponses(t *testing.T) {
	stream := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"second","arguments":"{\"v\":"}},{"index":0,"id":"a","type":"function","function":{"name":"first","arguments":"{\"v\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"9007199254740993}"}},{"index":1,"function":{"arguments":"true}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]
`
	_, calls, err := ai.ReadCompletion(strings.NewReader(stream), func(string) error { return nil })
	if err != nil || len(calls) != 2 || calls[0].ID != "a" || calls[0].Function.Arguments != `{"v":9007199254740993}` || calls[1].Function.Arguments != `{"v":true}` {
		t.Fatal(calls, err)
	}
	for _, broken := range []string{`data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}`, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"save_draft","arguments":"{"}}]},"finish_reason":"tool_calls"}]}`} {
		if _, _, err = ai.ReadCompletion(strings.NewReader(broken), func(string) error { return nil }); err == nil {
			t.Fatal("interrupted tool stream accepted")
		}
	}
}

func TestMCPAndChatThreeDraftKindsAndAuthorization(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, err := app.Open(ctx, app.Options{Mode: "cloud", NodeID: "cloud", DSN: filepath.Join(dir, "db"), KeyFile: filepath.Join(dir, "key"), BootstrapPassword: "test-password-1234", Seed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	mock := httptest.NewServer(aimock.Server{})
	defer mock.Close()
	a.Server.Chat.(*ai.Chat).Default = ai.ModelConfig{Endpoint: mock.URL + "/v1", Model: "sf-acceptance-simulator", TimeoutMS: 5000}
	aiToken, _, err := a.Server.Identity.Login(ctx, "assistant", "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	humanToken, _, err := a.Server.Identity.Login(ctx, "admin", "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	handler := a.Server.Handler()
	call := func(token, method, path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	rpc := func(path, method string, params any) *httptest.ResponseRecorder {
		return call(aiToken, "POST", path, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	}
	w := rpc("/mcp/read", "initialize", map[string]any{"protocolVersion": "2025-03-26"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"protocolVersion":"2025-03-26"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = rpc("/mcp/read", "tools/list", map[string]any{})
	if strings.Contains(w.Body.String(), `"name":"save_draft"`) {
		t.Fatal("readonly MCP advertises a write tool")
	}
	before, _ := a.Store.List(ctx, "definition")
	checkedKinds := map[string]bool{}
	for _, d := range app.ExampleDefinitions() {
		if checkedKinds[d.Kind] {
			continue
		}
		checkedKinds[d.Kind] = true
		kind := d.Kind
		d.ID = "mcp-" + kind
		d.GroupID = "factory"
		d.SchemaVersion = "1.0"
		d.Name = "MCP " + kind
		args := map[string]any{"draft": model.Draft{ID: d.ID, Definition: d}, "expected_version": 0}
		w = rpc("/mcp/read", "tools/call", map[string]any{"name": "save_draft", "arguments": args})
		if !strings.Contains(w.Body.String(), `"isError":true`) {
			t.Fatal("readonly MCP saved draft", w.Body.String())
		}
		for _, tool := range []string{"save_draft", "validate_draft", "diff_draft"} {
			var input any = map[string]any{"id": d.ID}
			if tool == "save_draft" {
				input = args
			}
			w = rpc("/mcp/draft", "tools/call", map[string]any{"name": tool, "arguments": input})
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"isError":false`) {
				t.Fatalf("%s %s: %d %s", kind, tool, w.Code, w.Body.String())
			}
		}
		// The external client must use the returned revision when modifying.
		doc, e := a.Store.Get(ctx, "draft", d.ID)
		if e != nil {
			t.Fatal(e)
		}
		draft, e := store.Decode[model.Draft](doc)
		if e != nil {
			t.Fatal(e)
		}
		draft.Definition.Name = "MCP modified " + kind
		w = rpc("/mcp/draft", "tools/call", map[string]any{"name": "save_draft", "arguments": map[string]any{"draft": draft, "expected_version": draft.Version}})
		if !strings.Contains(w.Body.String(), `"isError":false`) {
			t.Fatal(w.Body.String())
		}
		w = call(humanToken, "POST", "/api/sf/v1/assistant", map[string]any{"messages": []any{map[string]string{"role": "user", "content": "修改草稿 " + d.ID + " 名称=页面助手修改 " + kind}}})
		if !strings.Contains(w.Body.String(), "模拟草稿已修改") || strings.Contains(w.Body.String(), `"type":"error"`) {
			t.Fatal(w.Body.String())
		}
		doc, e = a.Store.Get(ctx, "draft", d.ID)
		if e != nil {
			t.Fatal(e)
		}
		draft, e = store.Decode[model.Draft](doc)
		if e != nil || draft.Version != 3 || draft.Definition.Name != "页面助手修改 "+kind {
			t.Fatal(draft, e)
		}
		w = call(humanToken, "POST", "/api/sf/v1/assistant", map[string]any{"messages": []any{map[string]string{"role": "user", "content": "创建 " + kind + " 草稿"}}})
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"type":"done"`) || !strings.Contains(w.Body.String(), "save_draft") || strings.Contains(w.Body.String(), `"type":"error"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = call(humanToken, "POST", "/api/sf/v1/assistant", map[string]any{"messages": []any{map[string]string{"role": "user", "content": "直接发布正式策略"}}})
	if !strings.Contains(w.Body.String(), "拒绝了请求") {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"/api/sf/v1/drafts/mcp-analysis/publish", "/api/sf/v1/executions", "/api/sf/v1/config"} {
		w = call(aiToken, "POST", path, map[string]any{})
		if w.Code != 403 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	after, _ := a.Store.List(ctx, "definition")
	if fmt.Sprint(len(before)) != fmt.Sprint(len(after)) || store.Hash(before) != store.Hash(after) {
		t.Fatal("AI altered a published definition")
	}
	drafts, _ := a.Store.List(ctx, "draft")
	if len(drafts) != len(before)+6 {
		t.Fatalf("expected %d fixture and six new drafts, got %d", len(before), len(drafts))
	}
}

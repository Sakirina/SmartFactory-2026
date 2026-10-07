package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/compatibility"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP keeps only immutable protocol/tool catalogues. Every HTTP request is
// authenticated again and carries its own principal to the application layer.
// Stateless transport also avoids retaining authorization after a user logout.
type MCP struct {
	Tools       *Tools
	once        sync.Once
	read, draft http.Handler
}

type mcpRequestKey struct{}

func (m *MCP) handler(drafts bool) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "smartfactory", Version: compatibility.Current().ApplicationVersion}, &mcp.ServerOptions{
		Instructions: "Discover the catalogue, describe an output, choose the permitted scope, then query. Draft changes require human publication in the platform UI.",
	})
	for _, tool := range ToolsList(drafts) {
		readOnly, _ := tool.Annotations["readOnlyHint"].(bool)
		no := false
		server.AddTool(&mcp.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: readOnly, DestructiveHint: &no, OpenWorldHint: &no}}, func(ctx context.Context, call *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result := &mcp.CallToolResult{}
			request, ok := ctx.Value(mcpRequestKey{}).(*http.Request)
			if !ok {
				result.SetError(http.ErrNoCookie)
				return result, nil
			}
			var args map[string]any
			if len(call.Params.Arguments) > 0 {
				if err := validateToolArguments(string(call.Params.Arguments)); err != nil {
					result.SetError(err)
					return result, nil
				}
				if err := store.DecodeJSON(call.Params.Arguments, &args); err != nil {
					result.SetError(err)
					return result, nil
				}
			}
			value, err := m.Tools.Call(request.WithContext(ctx), call.Params.Name, args, drafts)
			if err != nil {
				result.SetError(err)
				return result, nil
			}
			data, err := json.Marshal(value)
			if err != nil {
				result.SetError(err)
				return result, nil
			}
			result.Content = []mcp.Content{&mcp.TextContent{Text: string(data)}}
			result.StructuredContent = map[string]any{"result": value}
			return result, nil
		})
	}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 2 << 20, PropagateRequestCancellation: true})
}

func (m *MCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp/read" && r.URL.Path != "/mcp/draft" {
		http.NotFound(w, r)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	p, err := m.Tools.API.Authenticate(r)
	if err != nil {
		writeJSON(w, 401, map[string]string{"error": "authentication required"})
		return
	}
	if err = m.Tools.API.Identity.Permit(r.Context(), p, "read", ""); err != nil {
		writeJSON(w, 403, map[string]string{"error": "permission denied"})
		return
	}
	if !p.User.AI {
		writeJSON(w, 403, map[string]string{"error": "a dedicated AI identity is required for MCP"})
		return
	}
	drafts := !ReadOnlyPath(r.URL.Path) && m.Tools.API.Identity.Permit(r.Context(), p, "draft", "") == nil
	m.once.Do(func() { m.read = m.handler(false); m.draft = m.handler(true) })
	// Existing JSON-RPC clients omitted media-type negotiation. Explicit media
	// types and the protocol version remain subject to the SDK's validation.
	request := r.Clone(context.WithValue(r.Context(), mcpRequestKey{}, r))
	request.Header = r.Header.Clone()
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "application/json, text/event-stream")
	}
	if request.Method == http.MethodPost && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	handler := m.read
	if drafts {
		handler = m.draft
	}
	handler.ServeHTTP(w, request)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

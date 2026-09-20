package ai

import (
	"encoding/json"
	"net/http"
	"strings"

	"competition2026/product/platform/internal/api"
	"competition2026/product/platform/internal/store"
)

type MCP struct{ Tools *Tools }

func (m *MCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/mcp/read" && r.URL.Path != "/mcp/draft" {
		http.NotFound(w, r)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host && origin != "https://"+r.Host {
		http.Error(w, "origin not allowed", 403)
		return
	}
	p, e := m.Tools.API.Authenticate(r)
	if e != nil {
		writeJSON(w, 401, map[string]string{"error": "authentication required"})
		return
	}
	if e = m.Tools.API.Identity.Permit(r.Context(), p, "read", ""); e != nil {
		writeJSON(w, 403, map[string]string{"error": "permission denied"})
		return
	}
	if !p.User.AI {
		writeJSON(w, 403, map[string]string{"error": "a dedicated AI identity is required for MCP"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(405)
		return
	}
	version := r.Header.Get("MCP-Protocol-Version")
	if version != "" && version != "2025-03-26" && version != "2025-06-18" {
		writeJSON(w, 400, map[string]string{"error": "unsupported MCP protocol version"})
		return
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if e = d.Decode(&req); e != nil || req.JSONRPC != "2.0" {
		writeJSON(w, 400, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32600, "message": "invalid JSON-RPC request"}})
		return
	}
	if len(req.ID) == 0 {
		if strings.HasPrefix(req.Method, "notifications/") {
			w.WriteHeader(202)
			return
		}
		writeJSON(w, 400, map[string]string{"error": "request id is required"})
		return
	}
	drafts := !ReadOnlyPath(r.URL.Path) && m.Tools.API.Identity.Permit(r.Context(), p, "draft", "") == nil
	var result any
	var rpcError any
	switch req.Method {
	case "initialize":
		var init struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if e := json.Unmarshal(req.Params, &init); e != nil {
			rpcError = map[string]any{"code": -32602, "message": "invalid initialization parameters"}
			break
		}
		result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]string{"name": "smartfactory", "version": "0.1.0"}, "instructions": "Discover the catalogue, describe an output, choose the permitted scope, then query. Draft changes require human publication in the platform UI."}
		if init.ProtocolVersion == "2025-03-26" {
			result.(map[string]any)["protocolVersion"] = init.ProtocolVersion
		}
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": ToolsList(drafts)}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if e = store.DecodeJSON(req.Params, &params); e != nil {
			rpcError = map[string]any{"code": -32602, "message": "invalid tool arguments"}
			break
		}
		value, e := m.Tools.Call(r, params.Name, params.Arguments, drafts)
		text := ""
		if e != nil {
			text = e.Error()
		} else {
			b, _ := json.Marshal(value)
			text = string(b)
		}
		result = map[string]any{"content": []any{map[string]string{"type": "text", "text": text}}, "isError": e != nil}
		if e == nil {
			result.(map[string]any)["structuredContent"] = map[string]any{"result": value}
		}
	default:
		rpcError = map[string]any{"code": -32601, "message": "method not found"}
	}
	response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rpcError != nil {
		response["error"] = rpcError
	} else {
		response["result"] = result
	}
	writeJSON(w, 200, response)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

var _ = api.APIError{}

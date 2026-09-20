package ai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"competition2026/product/platform/internal/api"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
}
type Tools struct{ API *api.Server }

func object(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func ToolsList(drafts bool) []Tool {
	str := func(description string) any { return map[string]any{"type": "string", "description": description} }
	num := func(description string) any { return map[string]any{"type": "integer", "description": description} }
	list := []Tool{{Name: "discover_catalogue", Description: "Discover available processed outputs. Start here, then describe one output before querying values.", InputSchema: object(map[string]any{})}, {Name: "describe_output", Description: "Read the output schema, unit, scope, status and query fields for one catalogue entry.", InputSchema: object(map[string]any{"id": str("Catalogue entry ID")}, "id")}, {Name: "query_data", Description: "Query an explicit set of permitted devices and fields; returns quality, source status and revisions with values.", InputSchema: object(map[string]any{"device_ids": str("Comma-separated device or asset IDs"), "keys": str("Comma-separated field keys"), "from_ms": num("Inclusive UTC start timestamp in milliseconds"), "to_ms": num("Inclusive UTC end timestamp in milliseconds"), "resolution": str("raw, minute, hour or day"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}, "device_ids", "keys", "from_ms", "to_ms")}, {Name: "list_alarms", Description: "List visible alarms including activation, recovery, acknowledgement and historical status.", InputSchema: object(map[string]any{})}, {Name: "list_definitions", Description: "Discover visible analysis, alarm and strategy definitions without fetching all graphs.", InputSchema: object(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"analysis", "alarm", "strategy", ""}}})}, {Name: "get_definition", Description: "Read one versioned typed definition and its graph.", InputSchema: object(map[string]any{"id": str("Definition ID")}, "id")}, {Name: "list_drafts", Description: "List permitted unpublished drafts.", InputSchema: object(map[string]any{})}, {Name: "diff_draft", Description: "Compare a draft with its current published definition.", InputSchema: object(map[string]any{"id": str("Draft ID")}, "id")}, {Name: "explain_definition", Description: "Explain the node order, parameters, outputs and execution policy of a definition.", InputSchema: object(map[string]any{"id": str("Definition ID")}, "id")}}
	list = append(list, Tool{Name: "describe_definition_schema", Description: "Read the shared versioned schema and allowed node kinds before constructing analysis, alarm or strategy drafts.", InputSchema: object(map[string]any{})})
	if drafts {
		list = append(list, Tool{Name: "save_draft", Description: "Create or modify an analysis, alarm or strategy draft using the same typed object as the visual editor. expected_version provides optimistic concurrency. Formal publication is a human action.", InputSchema: object(map[string]any{"draft": map[string]any{"type": "object"}, "expected_version": num("Current draft revision; zero for creation")}, "draft", "expected_version")}, Tool{Name: "validate_draft", Description: "Validate graph types, expressions, dependencies and required control failure branches.", InputSchema: object(map[string]any{"id": str("Draft ID")}, "id")}, Tool{Name: "simulate_draft", Description: "Evaluate a draft against a provided sample and available history; has no device effects.", InputSchema: object(map[string]any{"id": str("Draft ID"), "point": map[string]any{"type": "object"}}, "id", "point")})
	}
	for i := range list {
		readOnly := list[i].Name != "save_draft"
		list[i].Annotations = map[string]any{"readOnlyHint": readOnly, "destructiveHint": false, "openWorldHint": false, "idempotentHint": readOnly}
	}
	return list
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,200}$`)

func (t *Tools) Call(r *http.Request, name string, args map[string]any, drafts bool) (any, error) {
	allowed := false
	for _, tool := range ToolsList(drafts) {
		if tool.Name == name {
			allowed = true
			if args == nil {
				args = map[string]any{}
			}
			if err := configcenter.Validate(tool.InputSchema, args); err != nil {
				return nil, err
			}
			for key := range args {
				props := tool.InputSchema["properties"].(map[string]any)
				if _, ok := props[key]; !ok {
					return nil, errors.New("unknown tool argument: " + key)
				}
			}
			break
		}
	}
	if !allowed {
		return nil, identity.ErrDenied
	}
	id, _ := args["id"].(string)
	if id != "" && !identifier.MatchString(id) {
		return nil, errors.New("invalid resource identifier")
	}
	switch name {
	case "describe_definition_schema":
		return t.API.CallAPI(r, "GET", "/api/sf/v1/contracts/Definition", nil)
	case "discover_catalogue":
		value, e := t.API.CallAPI(r, "GET", "/api/sf/v1/catalogue", nil)
		if e != nil {
			return nil, e
		}
		items, _ := value.([]any)
		out := []any{}
		for _, item := range items {
			m, _ := item.(map[string]any)
			out = append(out, map[string]any{"id": m["id"], "name": m["name"], "kind": m["kind"], "status": m["status"], "version": m["version"]})
		}
		return map[string]any{"entries": out, "next_step": "describe_output"}, nil
	case "describe_output":
		value, e := t.API.CallAPI(r, "GET", "/api/sf/v1/catalogue", nil)
		if e != nil {
			return nil, e
		}
		items, _ := value.([]any)
		for _, item := range items {
			m, _ := item.(map[string]any)
			if m["id"] == id {
				return m, nil
			}
		}
		return nil, store.ErrNotFound
	case "query_data":
		params := url.Values{}
		for _, key := range []string{"device_ids", "keys", "resolution"} {
			if v, ok := args[key].(string); ok {
				params.Set(key, v)
			}
		}
		for _, key := range []string{"from_ms", "to_ms", "limit"} {
			if v, ok := store.Number(args[key]); ok && v.IsInt() {
				params.Set(key, v.Num().String())
			}
		}
		limit, _ := strconv.Atoi(params.Get("limit"))
		if limit <= 0 || limit > 500 {
			params.Set("limit", "200")
		}
		if params.Get("device_ids") == "" || params.Get("keys") == "" {
			return nil, errors.New("query requires an explicit device and field scope")
		}
		return t.API.CallAPI(r, "GET", "/api/sf/v1/data?"+params.Encode(), nil)
	case "list_alarms":
		return t.API.CallAPI(r, "GET", "/api/sf/v1/alarms", nil)
	case "list_definitions", "get_definition", "explain_definition":
		value, e := t.API.CallAPI(r, "GET", "/api/sf/v1/definitions", nil)
		if e != nil {
			return nil, e
		}
		items, _ := value.([]any)
		out := []any{}
		for _, item := range items {
			m, _ := item.(map[string]any)
			if name == "list_definitions" {
				kind, _ := args["kind"].(string)
				if kind == "" || m["kind"] == kind {
					out = append(out, map[string]any{"id": m["id"], "name": m["name"], "kind": m["kind"], "status": m["status"], "version": m["version"]})
				}
			} else if m["id"] == id {
				if name == "get_definition" {
					return m, nil
				}
				return map[string]any{"name": m["name"], "kind": m["kind"], "selector": m["selector"], "nodes": m["nodes"], "connections": m["connections"], "outputs": m["outputs"], "policy": m["policy"], "status": m["status"]}, nil
			}
		}
		if name != "list_definitions" {
			return nil, store.ErrNotFound
		}
		return out, nil
	case "list_drafts":
		return t.API.CallAPI(r, "GET", "/api/sf/v1/drafts", nil)
	case "save_draft":
		raw, e := json.Marshal(args["draft"])
		if e != nil {
			return nil, e
		}
		var draft model.Draft
		if e = store.DecodeJSON(raw, &draft); e != nil {
			return nil, e
		}
		if !identifier.MatchString(draft.ID) || !identifier.MatchString(draft.Definition.ID) {
			return nil, errors.New("draft and definition IDs are required")
		}
		return t.API.CallAPI(r, "POST", "/api/sf/v1/drafts", args)
	case "validate_draft":
		return t.API.CallAPI(r, "POST", "/api/sf/v1/drafts/"+id+"/validate", map[string]any{})
	case "diff_draft":
		return t.API.CallAPI(r, "GET", "/api/sf/v1/drafts/"+id+"/diff", nil)
	case "simulate_draft":
		return t.API.CallAPI(r, "POST", "/api/sf/v1/drafts/"+id+"/simulate", map[string]any{"point": args["point"]})
	}
	return nil, errors.New("unknown tool")
}
func ChatTools(drafts bool) []any {
	out := []any{}
	for _, tool := range ToolsList(drafts) {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema}})
	}
	return out
}
func ReadOnlyPath(path string) bool { return strings.TrimSuffix(path, "/") == "/mcp/read" }

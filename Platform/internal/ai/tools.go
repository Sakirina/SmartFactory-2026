package ai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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
	ids := map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 256}
	page := func() map[string]any {
		return map[string]any{"after": str("Opaque next_after from the previous page under the same identity and filter"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}
	}
	definitionPage := page()
	definitionPage["kind"] = map[string]any{"type": "string", "enum": []string{"analysis", "alarm", "strategy", ""}}
	query := map[string]any{"resource_ids": ids, "definition_id": str("Definition ID filter"), "status": str("Business status filter"), "search": str("Search text"), "assignee_id": str("Assignee filter"), "handling_status": str("Handling status filter"), "active": map[string]any{"type": "boolean"}, "acknowledged": map[string]any{"type": "boolean"}, "from_ms": num("Inclusive UTC start time in milliseconds"), "to_ms": num("Inclusive UTC end time in milliseconds"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "page_token": str("Opaque next_page_token; preserve all filters")}
	list := []Tool{
		{Name: "discover_catalogue", Description: "Discover a bounded, authorized catalogue page. Read scope, has_more, next_after and budget_exhausted, then describe an output.", InputSchema: object(page())},
		{Name: "describe_output", Description: "Read a catalogue output schema, version, unit and resource scope.", InputSchema: object(map[string]any{"id": str("Catalogue entry ID")}, "id")},
		{Name: "query_data", Description: "Query a bounded trend page for explicit devices and fields. Retain scope, snapshot_cursor, next_page_token, has_more, quality and revisions.", InputSchema: object(map[string]any{"device_ids": str("Comma-separated device IDs"), "resource_ids": ids, "keys": str("Comma-separated field keys"), "from_ms": num("Inclusive UTC start timestamp"), "to_ms": num("Inclusive UTC end timestamp"), "resolution": str("raw, minute, hour or day"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "page_token": str("Next page token with unchanged scope")}, "keys", "from_ms", "to_ms")},
		{Name: "list_alarms", Description: "Read a filtered alarm QueryPage, including handling state, versions and pagination.", InputSchema: object(query)},
		{Name: "list_executions", Description: "Read a filtered execution QueryPage with committed revisions and pagination.", InputSchema: object(query)},
		{Name: "list_definitions", Description: "Read a bounded authorized definition page. Continue with next_after; an empty page may still have a continuation because access filtering precedes user-visible results.", InputSchema: object(definitionPage)},
		{Name: "get_definition", Description: "Read a definition and version after checking every selected resource and transitive dependency.", InputSchema: object(map[string]any{"id": str("Definition ID")}, "id")},
		{Name: "list_drafts", Description: "Read a bounded authorized draft page with scope and continuation.", InputSchema: object(page())},
		{Name: "diff_draft", Description: "Compare an authorized draft with the current published definition.", InputSchema: object(map[string]any{"id": str("Draft ID")}, "id")},
		{Name: "explain_definition", Description: "Read the definition identity, version, node order, parameters, outputs and execution policy.", InputSchema: object(map[string]any{"id": str("Definition ID")}, "id")},
		{Name: "describe_definition_schema", Description: "Read the shared definition schema and allowed node kinds.", InputSchema: object(map[string]any{})},
		{Name: "get_execution", Description: "Read an execution through its authorized application use case, including state, command evidence and feedback timeline.", InputSchema: object(map[string]any{"id": str("Execution ID")}, "id")},
		{Name: "get_analysis_run", Description: "Read an authorized replay, comparison or shadow run with pinned versions and execution state.", InputSchema: object(map[string]any{"id": str("Analysis run ID")}, "id")},
		{Name: "get_analysis_steps", Description: "Read a bounded authorized analysis step page. Continue with next_after and retain the run identity.", InputSchema: object(map[string]any{"id": str("Analysis run ID"), "after": num("Zero-based step continuation"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, "id")},
	}
	if drafts {
		list = append(list,
			Tool{Name: "save_draft", Description: "Save an analysis, alarm or strategy draft through the authorized application. expected_version is zero for creation or the current draft revision.", InputSchema: object(map[string]any{"draft": map[string]any{"type": "object"}, "expected_version": num("Current draft revision")}, "draft", "expected_version")},
			Tool{Name: "validate_draft", Description: "Validate an authorized draft without publishing or device effects.", InputSchema: object(map[string]any{"id": str("Draft ID")}, "id")},
			Tool{Name: "simulate_draft", Description: "Simulate an authorized draft using a sample without device effects.", InputSchema: object(map[string]any{"id": str("Draft ID"), "point": map[string]any{"type": "object"}}, "id", "point")})
	}
	for i := range list {
		readOnly := list[i].Name != "save_draft"
		list[i].Annotations = map[string]any{"readOnlyHint": readOnly, "destructiveHint": false, "openWorldHint": false, "idempotentHint": readOnly}
	}
	return list
}

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
	if _, hasID := args["id"]; hasID && !model.ResourceID(id) {
		return nil, errors.New("invalid resource identifier")
	}
	principal, err := t.API.Authenticate(r)
	if err != nil {
		return nil, err
	}
	if err = t.API.Identity.Permit(r.Context(), principal, "read", ""); err != nil {
		return nil, err
	}
	application := t.API.InvestigationApplication()
	scope := model.AIDocumentScope{}
	if name == "discover_catalogue" || name == "list_definitions" || name == "list_drafts" {
		raw, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		if err = store.DecodeJSON(raw, &scope); err != nil {
			return nil, err
		}
		collection := map[string]string{"discover_catalogue": "catalogue", "list_definitions": "definition", "list_drafts": "draft"}[name]
		return application.Documents(r.Context(), principal, collection, scope)
	}
	switch name {
	case "describe_definition_schema":
		return t.API.CallAPI(r, "GET", "/api/sf/v1/contracts/Definition", nil)
	case "describe_output":
		return application.ReadCatalogue(r.Context(), principal, id)
	case "query_data":
		query := model.QueryRequest{Limit: 200}
		queryArgs := map[string]any{}
		for key, value := range args {
			if key != "keys" && key != "device_ids" {
				queryArgs[key] = value
			}
		}
		raw, _ := json.Marshal(queryArgs)
		if err = store.DecodeJSON(raw, &query); err != nil {
			return nil, err
		}
		if devices, ok := args["device_ids"].(string); ok && devices != "" {
			if len(query.ResourceIDs) > 0 {
				return nil, errors.New("provide one device scope")
			}
			query.ResourceIDs = strings.Split(devices, ",")
		}
		keys, _ := args["keys"].(string)
		query.Keys = strings.Split(keys, ",")
		if len(query.ResourceIDs) == 0 || keys == "" {
			return nil, errors.New("query requires an explicit device and field scope")
		}
		for _, resource := range query.ResourceIDs {
			if err = t.API.Identity.Permit(r.Context(), principal, "read", resource); err != nil {
				return nil, err
			}
		}
		return t.API.CallAPI(r, "POST", "/api/sf/v1/queries/trend", query)
	case "list_alarms", "list_executions":
		kind := map[string]string{"list_alarms": "alarms", "list_executions": "executions"}[name]
		return t.API.CallAPI(r, "POST", "/api/sf/v1/queries/"+kind, args)
	case "get_definition", "explain_definition":
		return application.ReadDefinitionEvidence(r.Context(), principal, id)
	case "get_execution":
		return t.API.CallAPI(r, "GET", "/api/sf/v1/executions/"+url.PathEscape(id), nil)
	case "get_analysis_run":
		return t.API.CallAPI(r, "GET", "/api/sf/v1/analysis-runs/"+url.PathEscape(id), nil)
	case "get_analysis_steps":
		params := url.Values{}
		for _, key := range []string{"after", "limit"} {
			if n, ok := store.Number(args[key]); ok && n.IsInt() {
				params.Set(key, n.Num().String())
			}
		}
		result, err := t.API.CallAPI(r, "GET", "/api/sf/v1/analysis-runs/"+url.PathEscape(id)+"/steps?"+params.Encode(), nil)
		if err != nil {
			return nil, err
		}
		run, err := application.History.Get(r.Context(), principal, id)
		if err != nil {
			return nil, err
		}
		fields, ok := result.(map[string]any)
		if !ok {
			return nil, errors.New("invalid analysis steps response")
		}
		fields["run_version"] = run.Version
		fields["resources"] = run.Resources
		return fields, nil
	case "diff_draft":
		draft, err := application.ReadDraftEvidence(r.Context(), principal, id)
		if err != nil {
			return nil, err
		}
		var body model.Draft
		raw, _ := json.Marshal(draft)
		_ = store.DecodeJSON(raw, &body)
		additional := []model.EvidenceResource{}
		previous, err := application.ReadDefinitionEvidence(r.Context(), principal, body.Definition.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if previous != nil {
			additional, _ = previous["_resources"].([]model.EvidenceResource)
		}
		result, err := t.API.CallAPI(r, "GET", "/api/sf/v1/drafts/"+url.PathEscape(id)+"/diff", nil)
		if err != nil {
			return nil, err
		}
		return application.DecorateDraftResult(r.Context(), principal, id, body.Version, result, additional)
	case "save_draft":
		raw, e := json.Marshal(args["draft"])
		if e != nil {
			return nil, e
		}
		var draft model.Draft
		if e = store.DecodeJSON(raw, &draft); e != nil {
			return nil, e
		}
		if !model.ResourceID(draft.ID) || !model.ResourceID(draft.Definition.ID) {
			return nil, errors.New("draft and definition IDs are required")
		}
		result, err := t.API.CallAPI(r, "POST", "/api/sf/v1/drafts", args)
		if err != nil {
			return nil, err
		}
		raw, _ = json.Marshal(result)
		var saved model.Draft
		if err = store.DecodeJSON(raw, &saved); err != nil {
			return nil, err
		}
		return application.DecorateDraftResult(r.Context(), principal, saved.ID, saved.Version, result, nil)
	case "validate_draft", "simulate_draft":
		current, err := application.ReadDraftEvidence(r.Context(), principal, id)
		if err != nil {
			return nil, err
		}
		n, _ := store.Number(current["version"])
		version := n.Num().Int64()
		suffix := "validate"
		body := map[string]any{"expected_version": version}
		if name == "simulate_draft" {
			suffix = "simulate"
			body = map[string]any{"point": args["point"]}
		}
		result, err := t.API.CallAPI(r, "POST", "/api/sf/v1/drafts/"+url.PathEscape(id)+"/"+suffix, body)
		if err != nil {
			return nil, err
		}
		return application.DecorateDraftResult(r.Context(), principal, id, version, result, nil)
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

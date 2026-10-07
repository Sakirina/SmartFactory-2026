// sf-contracts exports wire schemas, typed operations and remaining legacy routes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"competition2026/product/platform/internal/api"
	"competition2026/product/platform/internal/app"
	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/plugins"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/compatibility"
	"competition2026/product/platform/pkg/contracts"
	"competition2026/product/platform/pkg/model"
)

type S = map[string]any

func main() {
	out := flag.String("out", "../contracts/1.0", "contract output directory")
	apiDir := flag.String("api-source", "internal/api", "business API source directory")
	examples := flag.String("examples", "../examples/factory/definitions.json", "editable example definitions")
	flag.Parse()
	if err := run(*out, *apiDir, *examples); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(out, apiDir, examples string) error {
	r := contracts.New()
	for _, value := range []any{configcenter.Parameter{}, plugins.OrganizationSync{}, plugins.Spec{}, plugins.Status{}, identity.Grant{}, store.IngestBatch{}, store.IngestResult{}, store.AuditEvent{}, store.Aggregate{}, application.SaveDraftInput{}, api.DraftRevisionRequest{}, api.ContractError{}, api.CompatibilityStatus{}} {
		r.Add(value)
	}
	for _, value := range api.TaskContractTypes() {
		r.Add(value)
	}
	for _, value := range api.RuleContractTypes() {
		r.Add(value)
	}
	for _, value := range api.HistoryContractTypes() {
		r.Add(value)
	}
	for _, value := range api.BusinessContractTypes() {
		r.Add(value)
	}
	for _, value := range api.QueryContractTypes() {
		r.Add(value)
	}
	for _, value := range api.ControlContractTypes() {
		r.Add(value)
	}
	for _, value := range api.InvestigationContractTypes() {
		r.Add(value)
	}
	for _, value := range api.NodeConfigurationContractTypes() {
		r.Add(value)
	}
	api.RefineNodeConfigurationContracts(r)
	for _, value := range api.ReleaseContractTypes() {
		r.Add(value)
	}
	api.RefineReleaseContracts(r)
	for _, name := range r.Names() {
		schema, _ := r.Schema(name)
		if e := writeJSON(filepath.Join(out, name+".schema.json"), schema); e != nil {
			return e
		}
	}
	spec, e := openAPI(apiDir, r)
	if e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(out, "openapi.json"), spec); e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(out, "..", "compatibility.json"), compatibility.Current()); e != nil {
		return e
	}
	defs := app.ExampleDefinitions()
	for i := range defs {
		defs[i].SchemaVersion = model.ContractVersion
		defs[i].GroupID = "factory"
		defs[i].Status = "draft"
	}
	if e = writeJSON(examples, S{"schema_version": model.ContractVersion, "definitions": defs}); e != nil {
		return e
	}
	fmt.Printf("Exported %d schema types, OpenAPI routes, compatibility manifest and %d editable definitions\n", len(r.Names()), len(defs))
	return nil
}
func writeJSON(path string, v any) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
func stringLiteral(e ast.Expr) string {
	if x, ok := e.(*ast.BasicLit); ok && x.Kind == token.STRING {
		s, _ := strconv.Unquote(x.Value)
		return s
	}
	return ""
}
func astSchema(e ast.Expr) S {
	switch t := e.(type) {
	case *ast.Ident:
		switch t.Name {
		case "string":
			return S{"type": "string"}
		case "bool":
			return S{"type": "boolean"}
		case "int", "int64", "uint64":
			return S{"type": "integer"}
		case "float64":
			return S{"type": "number"}
		default:
			return S{}
		}
	case *ast.SelectorExpr:
		return S{"$ref": "#/components/schemas/" + t.Sel.Name}
	case *ast.StarExpr:
		return S{"anyOf": []any{astSchema(t.X), S{"type": "null"}}}
	case *ast.ArrayType:
		return S{"type": []string{"array", "null"}, "items": astSchema(t.Elt)}
	case *ast.MapType:
		return S{"type": []string{"object", "null"}, "additionalProperties": astSchema(t.Value)}
	case *ast.StructType:
		p := S{}
		for _, f := range t.Fields.List {
			if f.Tag == nil {
				continue
			}
			tag, _ := strconv.Unquote(f.Tag.Value)
			key := strings.Split(reflect.StructTag(tag).Get("json"), ",")[0]
			if key != "" && key != "-" {
				p[key] = astSchema(f.Type)
			}
		}
		return S{"type": "object", "properties": p, "additionalProperties": false}
	default:
		return S{}
	}
}
func requestSchema(f *ast.FuncDecl) S {
	if f == nil {
		return nil
	}
	var result S
	ast.Inspect(f.Body, func(n ast.Node) bool {
		decl, ok := n.(*ast.ValueSpec)
		if !ok || len(decl.Names) != 1 {
			return true
		}
		if decl.Names[0].Name == "req" || decl.Names[0].Name == "batch" || decl.Names[0].Name == "job" {
			result = astSchema(decl.Type)
		}
		return true
	})
	return result
}
func openAPI(dir string, r *contracts.Registry) (S, error) {
	files, e := filepath.Glob(filepath.Join(dir, "*.go"))
	if e != nil {
		return nil, e
	}
	sort.Strings(files)
	functions := map[string]*ast.FuncDecl{}
	trees := []*ast.File{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		tree, e := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if e != nil {
			return nil, e
		}
		trees = append(trees, tree)
		for _, decl := range tree.Decls {
			if f, ok := decl.(*ast.FuncDecl); ok {
				functions[f.Name.Name] = f
			}
		}
	}
	paths := S{}
	responseTypes := map[string]string{"entities": "[]Entity", "assetProposals": "[]AssetProposal", "decideAssetProposal": "AssetProposal", "putEntity": "Entity", "data": "DataResult", "definitions": "[]Definition", "definitionVersions": "[]Definition", "drafts": "[]Draft", "deactivate": "Definition", "rollback": "Draft", "executions": "[]Execution", "createExecution": "Execution", "approve": "Execution", "dispatch": "Execution", "alarms": "[]Alarm", "ackAlarm": "Alarm", "jobs": "[]Job", "createJob": "Job", "retryJob": "Job", "putUser": "User", "putGrant": "Grant", "listConfig": "[]Parameter", "putConfig": "Parameter", "putPlugin": "Spec", "ingest": "IngestResult", "dashboards": "[]Dashboard", "putDashboard": "Dashboard"}
	add := func(pattern, action, name string) {
		parts := strings.SplitN(pattern, " ", 2)
		if len(parts) != 2 || !strings.HasPrefix(parts[1], "/api/sf/v1/") {
			return
		}
		method, path := strings.ToLower(parts[0]), strings.TrimPrefix(parts[1], "/api/sf/v1")
		responseSchema := S{}
		if name, ok := responseTypes[name]; ok {
			if strings.HasPrefix(name, "[]") {
				responseSchema = S{"type": "array", "items": S{"$ref": "#/components/schemas/" + name[2:]}}
			} else {
				responseSchema = S{"$ref": "#/components/schemas/" + name}
			}
		}
		responses := S{"200": S{"description": "Request completed", "content": S{"application/json": S{"schema": responseSchema}}}}
		if name == "createJob" || name == "retryJob" {
			delete(responses, "200")
			responses["202"] = S{"description": "Recomputation accepted", "content": S{"application/json": S{"schema": responseSchema}}}
		}
		if name == "events" {
			responses["200"] = S{"description": "Server-sent data snapshots; each data event contains DataResult", "content": S{"text/event-stream": S{"schema": S{"type": "string"}}}}
		}
		if name == "putEntity" {
			responses["202"] = S{"description": "Edge asset proposal accepted for cloud review", "content": S{"application/json": S{"schema": S{"$ref": "#/components/schemas/AssetProposal"}}}}
		}
		for _, status := range []string{"400", "401", "403", "404", "409", "504"} {
			responses[status] = S{"description": map[string]string{"400": "Invalid input or business condition", "401": "Authentication expired or invalid", "403": "Action or resource not permitted", "404": "Object unavailable", "409": "Version or content conflict", "504": "Operation timed out"}[status], "content": S{"application/json": S{"schema": S{"type": "object", "required": []string{"error"}, "properties": S{"error": S{"type": "string"}}}}}}
		}
		op := S{"operationId": method + "_" + strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_").Replace(strings.TrimPrefix(path, "/")), "summary": name, "x-permission-action": action, "x-contract-source": "legacy-source", "responses": responses}
		if action == "anonymous" {
			op["security"] = []any{}
		}
		parameters := []any{}
		for _, segment := range strings.Split(path, "/") {
			if strings.HasPrefix(segment, "{") {
				parameters = append(parameters, S{"name": strings.Trim(segment, "{}"), "in": "path", "required": true, "schema": S{"type": "string"}})
			}
		}
		if name == "data" || name == "events" {
			for _, key := range []string{"device_ids", "keys", "from_ms", "to_ms", "window_ms", "limit", "resolution", "include_latest"} {
				typ := "integer"
				if key == "include_latest" {
					typ = "boolean"
				}
				if key == "device_ids" || key == "keys" || key == "resolution" {
					typ = "string"
				}
				parameters = append(parameters, S{"name": key, "in": "query", "schema": S{"type": typ}, "description": map[string]string{"device_ids": "Comma-separated resource identifiers, each checked against the caller's grants", "keys": "Comma-separated field keys", "limit": "Total returned points, default 2000 and maximum 40000", "from_ms": "Inclusive observation time in UTC milliseconds", "to_ms": "Inclusive observation time in UTC milliseconds", "window_ms": "Relative window used when from_ms is omitted; default one hour", "resolution": "raw, minute, hour or day", "include_latest": "Include one latest value per authorized series independently of the trend window, up to 2000 series"}[key]})
			}
		}
		if len(parameters) > 0 {
			op["parameters"] = parameters
		}
		if body := requestSchema(functions[name]); body != nil {
			op["requestBody"] = S{"required": true, "content": S{"application/json": S{"schema": body}}}
		}
		p, ok := paths[path].(S)
		if !ok {
			p = S{}
			paths[path] = p
		}
		p[method] = op
	}
	for _, tree := range trees {
		ast.Inspect(tree, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			f, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if f.Sel.Name == "route" && len(call.Args) == 4 {
				pattern, action := stringLiteral(call.Args[1]), stringLiteral(call.Args[2])
				name := ""
				if h, ok := call.Args[3].(*ast.SelectorExpr); ok {
					name = h.Sel.Name
				}
				add(pattern, action, name)
			}
			return true
		})
	}
	add("POST /api/sf/v1/login", "anonymous", "login")
	defsRaw, _ := json.Marshal(r.Definitions)
	var definitions S
	if e = json.Unmarshal([]byte(strings.ReplaceAll(string(defsRaw), "#/$defs/", "#/components/schemas/")), &definitions); e != nil {
		return nil, e
	}
	// These operations come from the same generic registrations used by the
	// live server, including body, success, failure and path parameter types.
	typedJSON, e := json.Marshal(api.DefinitionOpenAPI())
	if e != nil {
		return nil, e
	}
	var typed S
	if e = json.Unmarshal(typedJSON, &typed); e != nil {
		return nil, e
	}
	for path, operations := range typed["paths"].(S) {
		shortPath := strings.TrimPrefix(path, "/api/sf/v1")
		pathItem, _ := paths[shortPath].(S)
		if pathItem == nil {
			pathItem = S{}
			paths[shortPath] = pathItem
		}
		for method, operation := range operations.(S) {
			pathItem[method] = operation
		}
	}
	for name, schema := range typed["components"].(S)["schemas"].(S) {
		definitions[name] = schema
	}
	return S{"openapi": "3.1.0", "jsonSchemaDialect": contracts.Dialect, "info": S{"title": "SmartFactory business API", "version": model.ContractVersion, "description": "Cloud and edge use the same versioned API. Token roles and every referenced resource are checked on the server. AI tokens can query and edit drafts. Native ThingsBoard, synchronization and configuration-service internal routes have separate credentials."}, "servers": []any{S{"url": "/api/sf/v1"}}, "security": []any{S{"bearerAuth": []string{}}}, "paths": paths, "components": S{"securitySchemes": S{"bearerAuth": S{"type": "http", "scheme": "bearer"}}, "schemas": definitions}}, nil
}

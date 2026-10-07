package contracts_test

import (
	"context"
	"reflect"
	"testing"

	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/pkg/contracts"
	"competition2026/product/platform/pkg/model"
)

func TestNodeCatalogFeedsSchemaAndCompilerParameterRules(t *testing.T) {
	registry := contracts.New()
	node := registry.Definitions["Node"]
	conditions := node["allOf"].([]any)
	catalog := model.NodeCatalog()
	if len(conditions) != len(catalog) {
		t.Fatal("schema omitted catalog nodes")
	}
	for i, metadata := range catalog {
		condition := conditions[i].(map[string]any)
		parameters := condition["then"].(map[string]any)["properties"].(map[string]any)["params"]
		if !reflect.DeepEqual(parameters, metadata.ParameterSchema) {
			t.Fatal("schema differs from node catalog", metadata.Type)
		}
	}
	definition := model.Definition{ID: "delay", Name: "Delay", Kind: "alarm", GroupID: "factory", SchemaVersion: model.ContractVersion, Nodes: []model.Node{{ID: "delay", Type: "debounce", Params: map[string]any{"duration_ms": 100, "mode": "activation"}}}}
	plan, validation := rulecore.Compile(context.Background(), definition)
	if !validation.Valid || plan.Nodes[0].Params.DurationMS != 100 || plan.Nodes[0].Params.Mode != "activation" {
		t.Fatal(plan, validation)
	}
	for _, params := range []map[string]any{{}, {"duration_ms": 0}, {"duration_ms": 86400001}, {"duration_ms": 1, "mode": "unknown"}, {"duration_ms": "1"}} {
		definition.Nodes[0].Params = params
		if _, validation := rulecore.Compile(context.Background(), definition); validation.Valid {
			t.Fatal("catalog constraint ignored", params)
		}
	}
}

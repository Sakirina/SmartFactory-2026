package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"competition2026/product/platform/internal/api"
	"competition2026/product/platform/pkg/compatibility"
)

func TestGeneratedContractsMatchLiveRegistration(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "1.0")
	if err := run(out, "../../internal/api", filepath.Join(root, "examples.json")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var generated S
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	liveJSON, err := json.Marshal(api.DefinitionOpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	var live S
	if err := json.Unmarshal(liveJSON, &live); err != nil {
		t.Fatal(err)
	}
	for path, value := range live["paths"].(S) {
		for method, operation := range value.(S) {
			generatedPath := generated["paths"].(S)[strings.TrimPrefix(path, "/api/sf/v1")].(S)
			if !reflect.DeepEqual(generatedPath[method], operation) {
				t.Errorf("registered %s %s differs from generated contract", method, path)
			}
		}
	}
	for _, path := range []string{"/jobs", "/jobs/{id}/retry"} {
		responses := generated["paths"].(S)[path].(S)["post"].(S)["responses"].(S)
		accepted, ok := responses["202"].(S)
		if !ok || responses["200"] != nil {
			t.Fatalf("legacy job operation must document HTTP 202: %s %+v", path, responses)
		}
		schema := accepted["content"].(S)["application/json"].(S)["schema"].(S)
		if schema["$ref"] != "#/components/schemas/Job" {
			t.Fatal(path, schema)
		}
	}
	data, err = os.ReadFile(filepath.Join(root, "compatibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest compatibility.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil || !reflect.DeepEqual(manifest, compatibility.Current()) {
		t.Fatalf("compatibility export: %+v %v", manifest, err)
	}
}

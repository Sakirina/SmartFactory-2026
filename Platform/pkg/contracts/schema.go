// Package contracts publishes JSON Schema from the actual serialized Go models.
package contracts

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"competition2026/product/platform/pkg/model"
)

const Dialect = "https://json-schema.org/draft/2020-12/schema"

type Schema = map[string]any
type Registry struct {
	Definitions map[string]Schema
	types       map[reflect.Type]string
}

func New() *Registry {
	r := &Registry{Definitions: map[string]Schema{}, types: map[reflect.Type]string{}}
	for _, value := range []any{model.Entity{}, model.Observation{}, model.DataResult{}, model.Definition{}, model.Draft{}, model.Validation{}, model.Execution{}, model.Alarm{}, model.Job{}, model.User{}, model.AssetProposal{}, model.Dashboard{}} {
		r.Add(value)
	}
	r.refine()
	return r
}

func (r *Registry) Add(value any) string {
	t := reflect.TypeOf(value)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	r.shape(t)
	return r.types[t]
}
func (r *Registry) shape(t reflect.Type) Schema {
	if t == reflect.TypeOf(json.RawMessage{}) {
		return Schema{}
	}
	if t == reflect.TypeOf(json.Number("")) {
		return Schema{"type": "number"}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return Schema{"anyOf": []any{r.shape(t.Elem()), Schema{"type": "null"}}}
	case reflect.Interface:
		return Schema{}
	case reflect.String:
		return Schema{"type": "string"}
	case reflect.Bool:
		return Schema{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return Schema{"type": "integer", "format": "int64"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Schema{"type": "integer", "minimum": 0, "format": "uint64"}
	case reflect.Float32, reflect.Float64:
		return Schema{"type": "number"}
	case reflect.Slice, reflect.Array:
		return Schema{"type": []string{"array", "null"}, "items": r.shape(t.Elem())}
	case reflect.Map:
		return Schema{"type": []string{"object", "null"}, "additionalProperties": r.shape(t.Elem())}
	case reflect.Struct:
		if name, ok := r.types[t]; ok {
			return Schema{"$ref": "#/$defs/" + name}
		}
		name := t.Name()
		if name == "" {
			panic("contracts: unnamed model type")
		}
		if _, exists := r.Definitions[name]; exists {
			panic("contracts: duplicate model name " + name)
		}
		r.types[t] = name
		properties, required := Schema{}, []string{}
		r.Definitions[name] = Schema{"type": "object", "properties": properties, "additionalProperties": false}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			parts := strings.Split(field.Tag.Get("json"), ",")
			if field.PkgPath != "" || parts[0] == "-" {
				continue
			}
			key := parts[0]
			if key == "" {
				key = field.Name
			}
			properties[key] = r.shape(field.Type)
			if !strings.Contains(field.Tag.Get("json"), "omitempty") {
				required = append(required, key)
			}
		}
		r.Definitions[name]["required"] = required
		return Schema{"$ref": "#/$defs/" + name}
	default:
		panic(fmt.Sprintf("contracts: unsupported type %v", t))
	}
}

func (r *Registry) Names() []string {
	names := []string{}
	for name := range r.Definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func (r *Registry) Schema(name string) (Schema, bool) {
	if _, ok := r.Definitions[name]; !ok {
		return nil, false
	}
	definitions := map[string]Schema{}
	var include func(string)
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok && strings.HasPrefix(ref, "#/$defs/") {
				include(strings.TrimPrefix(ref, "#/$defs/"))
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	include = func(key string) {
		if _, ok := definitions[key]; ok {
			return
		}
		definitions[key] = r.Definitions[key]
		walk(r.Definitions[key])
	}
	include(name)
	return Schema{"$schema": Dialect, "$id": "https://smartfactory.local/contracts/" + model.ContractVersion + "/" + name + ".schema.json", "title": name, "$ref": "#/$defs/" + name, "$defs": definitions}, true
}
func (r *Registry) property(name, key string) Schema {
	return r.Definitions[name]["properties"].(Schema)[key].(Schema)
}
func (r *Registry) refine() {
	r.property("Definition", "schema_version")["const"] = model.ContractVersion
	r.property("Definition", "kind")["enum"] = []string{"analysis", "alarm", "strategy"}
	r.property("Definition", "status")["enum"] = []string{"", "draft", "published", "inactive"}
	for _, key := range []string{"id", "name", "group_id"} {
		r.property("Definition", key)["minLength"] = 1
	}
	r.property("Definition", "nodes")["type"] = "array"
	r.property("Definition", "nodes")["minItems"] = 1
	r.property("Definition", "nodes")["maxItems"] = 128
	r.property("Definition", "connections")["maxItems"] = 512
	r.property("Definition", "version")["minimum"] = 0
	r.property("Observation", "quality")["enum"] = []string{"GOOD", "BAD", "UNCERTAIN"}
	r.property("QualitySummary", "completeness")["enum"] = []string{"unknown", "complete", "incomplete"}
	var conditions []any
	for _, kind := range []string{"analysis", "alarm", "strategy"} {
		nodes := []string{}
		for node, kinds := range model.NodeKinds() {
			for _, allowed := range kinds {
				if kind == allowed {
					nodes = append(nodes, node)
				}
			}
		}
		sort.Strings(nodes)
		conditions = append(conditions, Schema{"if": Schema{"properties": Schema{"kind": Schema{"const": kind}}}, "then": Schema{"properties": Schema{"nodes": Schema{"items": Schema{"properties": Schema{"type": Schema{"enum": nodes}}}}}}})
	}
	r.Definitions["Definition"]["allOf"] = conditions
	r.Definitions["Definition"]["description"] = "Versioned analysis, alarm or strategy graph. POST draft validate additionally checks references, dependencies, expression limits and control failure branches."
	r.property("Observation", "value")["description"] = "Lossless JSON value. Clients must preserve integers outside the JavaScript safe integer range."
	r.property("Dashboard", "metrics")["maxItems"] = 12
	r.property("Dashboard", "device_ids")["maxItems"] = 20
}

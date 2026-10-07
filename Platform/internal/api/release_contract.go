package api

import (
	"reflect"
	"strconv"
	"strings"

	"competition2026/product/platform/pkg/contracts"
)

// RefineReleaseContracts retains the validation annotations used by the live
// typed routes in the standalone schemas shipped to downstream consumers.
func RefineReleaseContracts(registry *contracts.Registry) {
	for _, value := range ReleaseContractTypes() {
		typ := reflect.TypeOf(value)
		name := registry.Add(value)
		properties := registry.Definitions[name]["properties"].(contracts.Schema)
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			property, ok := properties[name].(contracts.Schema)
			if !ok {
				continue
			}
			if enum := field.Tag.Get("enum"); enum != "" {
				property["enum"] = strings.Split(enum, ",")
			}
			for _, annotation := range []string{"minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems"} {
				if text := field.Tag.Get(annotation); text != "" {
					if number, err := strconv.ParseFloat(text, 64); err == nil {
						property[annotation] = number
					}
				}
			}
			for _, annotation := range []string{"readOnly", "writeOnly"} {
				if field.Tag.Get(annotation) == "true" {
					property[annotation] = true
				}
			}
		}
	}
}

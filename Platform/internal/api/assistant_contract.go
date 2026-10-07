package api

import (
	"net/http"
	"reflect"

	"competition2026/product/platform/pkg/model"
	"github.com/danielgtaylor/huma/v2"
)

// The existing streaming handler owns the HTTP route; this registration shares
// its request type and SSE payload schema with exported and live contracts.
func registerAssistantContract(api huma.API) {
	schemas := api.OpenAPI().Components.Schemas
	request := schemas.Schema(reflect.TypeFor[model.AssistantRequest](), true, "AssistantRequest")
	event := schemas.Schema(reflect.TypeFor[model.AssistantEvent](), true, "AssistantEvent")
	failure := schemas.Schema(reflect.TypeFor[ContractError](), true, "ContractError")
	responses := map[string]*huma.Response{"200": {Description: "UTF-8 SSE; each data field contains AssistantEvent", Content: map[string]*huma.MediaType{"text/event-stream": {Schema: event}}}}
	for _, status := range []string{"400", "401", "403", "503"} {
		responses[status] = &huma.Response{Description: "Request validation, current authorization or model configuration failed", Content: map[string]*huma.MediaType{"application/json": {Schema: failure}}}
	}
	api.OpenAPI().AddOperation(&huma.Operation{OperationID: "post_assistant", Method: http.MethodPost, Path: "/api/sf/v1/assistant", Summary: "Run a persisted AI investigation", RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: request}}}, Responses: responses, Extensions: map[string]any{"x-permission-action": "read", "x-contract-source": "typed-streaming", "x-max-request-bytes": 256 << 10}})
}

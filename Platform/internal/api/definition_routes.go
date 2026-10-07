package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/compatibility"
	"competition2026/product/platform/pkg/contracts"
	"competition2026/product/platform/pkg/model"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type SaveDraftRequest struct {
	Body application.SaveDraftInput
}

type DraftRevisionRequest struct {
	ExpectedVersion *int64 `json:"expected_version,omitempty" minimum:"0"`
}

func (DraftRevisionRequest) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	schema.Nullable = true
	return schema
}

type DraftOperationRequest struct {
	ID   string                `path:"id" minLength:"1"`
	Body *DraftRevisionRequest `required:"false"`
}

type DraftResponse struct {
	Body model.Draft
}

type ValidationResponse struct {
	Body model.Validation
}

type DefinitionResponse struct {
	Body model.Definition
}

type CompatibilityStatus struct {
	Manifest   compatibility.Manifest  `json:"manifest"`
	Driver     string                  `json:"driver"`
	Migrations []store.MigrationRecord `json:"migrations"`
}

type CompatibilityResponse struct {
	Body CompatibilityStatus
}

type ContractError struct {
	Message   string              `json:"error"`
	Code      string              `json:"code,omitempty"`
	Clear     bool                `json:"clear,omitempty"`
	Retryable bool                `json:"retryable,omitempty"`
	Details   []*huma.ErrorDetail `json:"details,omitempty"`
	status    int
}

func (e *ContractError) Error() string  { return e.Message }
func (e *ContractError) GetStatus() int { return e.status }

type definitionPrincipalKey struct{}

func contractError(err error) error {
	if err == nil {
		return nil
	}
	out := &ContractError{Message: err.Error(), status: errorStatus(err)}
	for _, queryErr := range []error{store.ErrQueryExpired, store.ErrQueryReset, store.ErrQueryAuthorization, store.ErrQueryChanged, store.ErrQueryInvalid, store.ErrQueryBudget} {
		if errors.Is(err, queryErr) {
			out.Code = queryErr.Error()
			out.Clear = queryErr != store.ErrQueryBudget
			out.Retryable = true
			break
		}
	}
	return out
}

func definitionAPI(mux *http.ServeMux) huma.API {
	config := huma.Config{
		OpenAPI: &huma.OpenAPI{
			OpenAPI: "3.1.0", JSONSchemaDialect: contracts.Dialect,
			Info: &huma.Info{Title: "SmartFactory definition application API", Version: model.ContractVersion},
			Components: &huma.Components{
				Schemas:         huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer),
				SecuritySchemes: map[string]*huma.SecurityScheme{"bearerAuth": {Type: "http", Scheme: "bearer"}},
			},
			Security: []map[string][]string{{"bearerAuth": {}}},
		},
		Formats: map[string]huma.Format{"application/json": {
			Marshal: func(w io.Writer, value any) error { return json.NewEncoder(w).Encode(value) },
			Unmarshal: func(data []byte, value any) error {
				decoder := json.NewDecoder(bytes.NewReader(data))
				decoder.UseNumber()
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(value); err != nil {
					return err
				}
				var extra any
				if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
					return errors.New("one JSON value is required")
				}
				return nil
			},
		}},
		DefaultFormat: "application/json",
		Transformers: []huma.Transformer{func(ctx huma.Context, _ string, value any) (any, error) {
			if problem, ok := value.(*huma.ErrorModel); ok {
				ctx.SetHeader("Content-Type", "application/json")
				return &ContractError{Message: problem.Detail, Details: publicValidationDetails(problem.Errors), status: problem.Status}, nil
			}
			return value, nil
		}},
	}
	config.Formats["json"] = config.Formats["application/json"]
	// Drafts can contain unfinished definitions. Schema validation checks fields
	// that are supplied; the validate/publish use cases apply graph requirements.
	config.FieldsOptionalByDefault = true
	return humago.New(mux, config)
}

func publicValidationDetails(details []*huma.ErrorDetail) []*huma.ErrorDetail {
	public := make([]*huma.ErrorDetail, 0, len(details))
	for _, detail := range details {
		if detail != nil {
			// Parent validation failures can carry the complete request body,
			// including write-only credentials and nested secret configuration.
			public = append(public, &huma.ErrorDetail{Message: detail.Message, Location: detail.Location})
		}
	}
	return public
}

func (s *Server) registerDefinitionRoutes(mux *http.ServeMux) huma.API {
	api := definitionAPI(mux)
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		request, writer := humago.Unwrap(ctx)
		principal, err := s.Authenticate(request)
		if err == nil {
			action, _ := ctx.Operation().Extensions["x-permission-action"].(string)
			err = s.Identity.Permit(request.Context(), principal, action, "")
		}
		if err != nil {
			fail(writer, err)
			return
		}
		operationCtx := observability.ExtractHTTP(request.Context(), request.Header)
		operationCtx = observability.WithIdentity(operationCtx, observability.Identity{RequestID: request.Header.Get("X-Request-ID"), ActorID: principal.Actor.UserID})
		next(huma.WithContext(ctx, context.WithValue(operationCtx, definitionPrincipalKey{}, principal)))
	})
	operation := func(id, path, action string) huma.Operation {
		responses := map[string]*huma.Response{}
		errorSchema := api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[ContractError](), true, "ContractError")
		for _, status := range []int{400, 401, 403, 404, 409, 410, 413, 422, 504} {
			responses[strconv.Itoa(status)] = &huma.Response{Description: http.StatusText(status), Content: map[string]*huma.MediaType{"application/json": {Schema: errorSchema}}}
		}
		return huma.Operation{OperationID: id, Method: http.MethodPost, Path: "/api/sf/v1" + path, Summary: id, MaxBodyBytes: 2 << 20, Responses: responses, Extensions: map[string]any{"x-permission-action": action, "x-contract-source": "typed-registration"}}
	}
	huma.Register(api, operation("post_drafts", "/drafts", "draft"), func(ctx context.Context, input *SaveDraftRequest) (*DraftResponse, error) {
		principal, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		draft, err := s.DefinitionApplication().SaveDraft(ctx, principal, input.Body)
		return &DraftResponse{Body: draft}, contractError(err)
	})
	huma.Register(api, operation("post_drafts_id_validate", "/drafts/{id}/validate", "draft"), func(ctx context.Context, input *DraftOperationRequest) (*ValidationResponse, error) {
		principal, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		request := application.DraftInput{ID: input.ID}
		if input.Body != nil {
			request.ExpectedVersion = input.Body.ExpectedVersion
		}
		validation, err := s.DefinitionApplication().ValidateDraft(ctx, principal, request)
		return &ValidationResponse{Body: validation}, contractError(err)
	})
	huma.Register(api, operation("post_drafts_id_publish", "/drafts/{id}/publish", "publish"), func(ctx context.Context, input *DraftOperationRequest) (*DefinitionResponse, error) {
		principal, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		request := application.DraftInput{ID: input.ID}
		if input.Body != nil {
			request.ExpectedVersion = input.Body.ExpectedVersion
		}
		definition, err := s.DefinitionApplication().PublishDraft(ctx, principal, request)
		return &DefinitionResponse{Body: definition}, contractError(err)
	})
	compatibilityOperation := operation("get_compatibility", "/compatibility", "read")
	compatibilityOperation.Method = http.MethodGet
	huma.Register(api, compatibilityOperation, func(ctx context.Context, _ *struct{}) (*CompatibilityResponse, error) {
		records, err := s.Store.MigrationRecords(ctx)
		if err != nil {
			return nil, contractError(err)
		}
		return &CompatibilityResponse{Body: CompatibilityStatus{Manifest: compatibility.Current(), Driver: s.Store.Driver, Migrations: records}}, nil
	})
	s.registerRuleRoutes(api, operation)
	s.registerTaskRoutes(api, operation)
	s.registerHistoryRoutes(api, operation)
	s.registerControlRoutes(api, operation)
	s.registerQueryRoutes(api, operation)
	s.registerBusinessRoutes(api, operation)
	s.registerInvestigationRoutes(api, operation)
	s.registerNodeConfigurationRoutes(api, operation)
	s.registerReleaseRoutes(api, operation)
	registerAssistantContract(api)
	return api
}

// DefinitionOpenAPI executes the same registration as the live HTTP server.
func DefinitionOpenAPI() *huma.OpenAPI {
	return (&Server{}).registerDefinitionRoutes(http.NewServeMux()).OpenAPI()
}

package api

import (
	"context"
	"net/http"
	"reflect"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/pkg/model"
	"github.com/danielgtaylor/huma/v2"
)

type CreateExecutionBody struct {
	DefinitionID string            `json:"definition_id" minLength:"1"`
	Params       map[string]string `json:"params"`
	Override     bool              `json:"override"`
	DownlinkID   string            `json:"downlink_id,omitempty"`
}
type CreateExecutionRequest struct{ Body CreateExecutionBody }
type ExecutionRequest struct {
	ID string `path:"id" minLength:"1"`
}
type ExecutionResponse struct{ Body model.Execution }
type ExecutionDetailResponse struct{ Body model.ExecutionDetail }
type ApprovalBody struct {
	Role            string `json:"role" enum:"engineer,leader,safety"`
	ExpectedVersion *int64 `json:"expected_version,omitempty" minimum:"1"`
}
type ApprovalRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body ApprovalBody
}
type ExecutionRevisionBody struct {
	ExpectedVersion *int64 `json:"expected_version,omitempty" minimum:"1"`
}

func (ExecutionRevisionBody) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	schema.Nullable = true
	return schema
}

type DispatchExecutionRequest struct {
	ID   string                 `path:"id" minLength:"1"`
	Body *ExecutionRevisionBody `required:"false"`
}
type ExecutionActionRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body model.ExecutionAction
}
type ControlOperationRequest struct {
	ID string `path:"id" minLength:"1"`
}
type ControlOperationResponse struct {
	Status int
	Body   model.ControlOperation
}

func ControlContractTypes() []any {
	return []any{model.Execution{}, model.ExecutionTransition{}, model.CommandEvidence{}, model.ExecutionAction{}, model.ExecutionDetail{}, model.ExecutionTimelineEntry{}, model.ExecutionAllowedAction{}, model.ControlOperation{}, CreateExecutionBody{}, ApprovalBody{}, ExecutionRevisionBody{}}
}

func (s *Server) registerControlRoutes(api huma.API, operation func(string, string, string) huma.Operation) {
	huma.Register(api, operation("post_executions", "/executions", "control"), func(ctx context.Context, in *CreateExecutionRequest) (*ExecutionResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		out, err := s.Control.Create(ctx, p, in.Body.DefinitionID, in.Body.Params, in.Body.Override, in.Body.DownlinkID)
		return &ExecutionResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_executions_id_approve", "/executions/{id}/approve", "approve"), func(ctx context.Context, in *ApprovalRequest) (*ExecutionResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		out, err := s.Control.ApproveVersion(ctx, p, in.ID, in.Body.Role, in.Body.ExpectedVersion)
		return &ExecutionResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_executions_id_dispatch", "/executions/{id}/dispatch", "control"), func(ctx context.Context, in *DispatchExecutionRequest) (*ExecutionResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		var version *int64
		if in.Body != nil {
			version = in.Body.ExpectedVersion
		}
		out, err := s.Control.DispatchVersion(ctx, p, in.ID, version)
		return &ExecutionResponse{Body: out}, contractError(err)
	})
	detail := operation("get_executions_id", "/executions/{id}", "read")
	detail.Method = http.MethodGet
	huma.Register(api, detail, func(ctx context.Context, in *ExecutionRequest) (*ExecutionDetailResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		out, err := s.Control.Detail(ctx, p, in.ID)
		return &ExecutionDetailResponse{Body: out}, contractError(err)
	})
	for _, action := range []string{"cancel", "reconcile", "resume"} {
		op := operation("post_executions_id_"+action, "/executions/{id}/"+action, "control")
		op.Description = "Returns a durable operation. Cloud requests return 202 pending and require both local expected_version and expected_source_version from execution detail. The target edge rechecks current authorization, state and versions before acting."
		op.Responses["202"] = &huma.Response{Description: "Operation durably queued for its target edge", Content: map[string]*huma.MediaType{"application/json": {Schema: api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[model.ControlOperation](), true, "ControlOperation")}}}
		huma.Register(api, op, func(ctx context.Context, in *ExecutionActionRequest) (*ControlOperationResponse, error) {
			p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
			out, err := s.Control.SubmitOperation(ctx, p, in.ID, action, in.Body)
			status := http.StatusOK
			if out.Status == "pending" {
				status = http.StatusAccepted
			}
			return &ControlOperationResponse{Status: status, Body: out}, contractError(err)
		})
	}
	operationDetail := operation("get_control_operations_id", "/control-operations/{id}", "read")
	operationDetail.Method = http.MethodGet
	huma.Register(api, operationDetail, func(ctx context.Context, in *ControlOperationRequest) (*ControlOperationResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		out, err := s.Control.Operation(ctx, p, in.ID)
		return &ControlOperationResponse{Status: http.StatusOK, Body: out}, contractError(err)
	})
}

package api

import (
	"context"
	"net/http"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/scenetemplates"
	"competition2026/product/platform/pkg/model"
	"github.com/danielgtaylor/huma/v2"
)

type BusinessIDRequest struct {
	ID string `path:"id" minLength:"1"`
}
type AlarmDetailResponse struct{ Body model.AlarmDetail }
type AlarmActionRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body application.AlarmActionInput
}
type WorkOrdersResponse struct{ Body []model.WorkOrder }
type WorkOrderDetailResponse struct{ Body model.WorkOrderDetail }
type CreateWorkOrderRequest struct {
	Body application.CreateWorkOrderInput
}
type WorkOrderActionRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body application.WorkOrderActionInput
}
type HandoverRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body application.HandoverInput
}
type SemanticRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body application.SemanticInput
}
type SemanticResponse struct{ Body model.SemanticDifference }
type ImpactRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body application.ImpactInput
}
type ImpactResponse struct{ Body model.ImpactAnalysis }
type ProtocolMetadataResponse struct {
	Body []deviceconfig.ProtocolMetadata
}
type ValidateDeviceConfigurationRequest struct{ Body deviceconfig.Request }
type ValidateDeviceConfigurationResponse struct{ Body deviceconfig.Result }
type DeviceConfigurationResponse struct {
	Body application.DeviceConfigurationDetail
}
type SaveDeviceConfigurationRequest struct {
	Body application.SaveDeviceConfigurationInput
}
type ConnectorConfigurationsResponse struct {
	Body []model.ConnectorConfigurationDetail
}
type ConnectorConfigurationResponse struct {
	Body model.ConnectorConfigurationDetail
}
type SaveConnectorConfigurationRequest struct {
	Body application.SaveConnectorConfigurationInput
}
type TemplatesResponse struct{ Body []scenetemplates.Template }
type PrepareTemplateBatchRequest struct{ Body model.TemplateBatchInput }
type TemplateBatchResponse struct{ Body model.TemplateBatch }
type RetryTemplateBatchRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body application.RetryTemplateBatchInput
}

func (s *Server) BusinessApplication() *application.Business {
	return &application.Business{Store: s.Store, Identity: s.Identity, Definitions: s.DefinitionApplication(), NodeID: s.NodeID, Mode: s.Mode}
}

func BusinessContractTypes() []any {
	return []any{model.BusinessAction{}, model.AlarmOperation{}, model.AlarmCase{}, model.AlarmDetail{}, application.AlarmActionInput{}, model.NativeAlarmState{}, model.NativeAlarmUpdate{}, model.WorkOrder{}, model.WorkOrderDetail{}, model.BusinessEntry{}, model.Handover{}, model.HandoverEvidence{}, application.CreateWorkOrderInput{}, application.WorkOrderActionInput{}, application.HandoverInput{}, application.SemanticInput{}, application.ImpactInput{}, model.SemanticIdentity{}, model.SemanticDifference{}, model.SemanticChange{}, model.ImpactAnalysis{}, deviceconfig.Field{}, deviceconfig.Section{}, deviceconfig.ProtocolMetadata{}, deviceconfig.Parameters{}, deviceconfig.Request{}, deviceconfig.Result{}, deviceconfig.Issue{}, application.SaveDeviceConfigurationInput{}, application.DeviceConfigurationDetail{}, application.SaveConnectorConfigurationInput{}, model.ConnectorConfiguration{}, model.ConnectorConfigurationReceipt{}, model.ConnectorConfigurationDetail{}, scenetemplates.Template{}, model.TemplateInstance{}, model.TemplateBatchInput{}, model.TemplateBatch{}, model.TemplateBinding{}, model.TemplateFailure{}, model.TemplateBatchAttempt{}, application.RetryTemplateBatchInput{}}
}

func (s *Server) registerBusinessRoutes(api huma.API, operation func(string, string, string) huma.Operation) {
	principal := func(ctx context.Context) identity.Principal {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		return p
	}
	get := func(id, path string) huma.Operation {
		op := operation(id, path, "read")
		op.Method = http.MethodGet
		return op
	}
	huma.Register(api, get("get_alarms_id", "/alarms/{id}"), func(ctx context.Context, in *BusinessIDRequest) (*AlarmDetailResponse, error) {
		out, err := s.BusinessApplication().AlarmDetail(ctx, principal(ctx), in.ID)
		return &AlarmDetailResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_alarms_id_actions", "/alarms/{id}/actions", "approve"), func(ctx context.Context, in *AlarmActionRequest) (*AlarmDetailResponse, error) {
		out, err := s.BusinessApplication().ActOnAlarm(ctx, principal(ctx), in.ID, in.Body)
		return &AlarmDetailResponse{Body: out}, contractError(err)
	})
	huma.Register(api, get("get_work_orders", "/work-orders"), func(ctx context.Context, _ *struct{}) (*WorkOrdersResponse, error) {
		out, err := s.BusinessApplication().WorkOrders(ctx, principal(ctx))
		return &WorkOrdersResponse{Body: out}, contractError(err)
	})
	huma.Register(api, get("get_work_orders_id", "/work-orders/{id}"), func(ctx context.Context, in *BusinessIDRequest) (*WorkOrderDetailResponse, error) {
		out, err := s.BusinessApplication().WorkOrderDetail(ctx, principal(ctx), in.ID)
		return &WorkOrderDetailResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_work_orders", "/work-orders", "approve"), func(ctx context.Context, in *CreateWorkOrderRequest) (*WorkOrderDetailResponse, error) {
		out, err := s.BusinessApplication().CreateWorkOrder(ctx, principal(ctx), in.Body)
		return &WorkOrderDetailResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_work_orders_id_actions", "/work-orders/{id}/actions", "approve"), func(ctx context.Context, in *WorkOrderActionRequest) (*WorkOrderDetailResponse, error) {
		out, err := s.BusinessApplication().ChangeWorkOrder(ctx, principal(ctx), in.ID, in.Body)
		return &WorkOrderDetailResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_work_orders_id_handovers", "/work-orders/{id}/handovers", "approve"), func(ctx context.Context, in *HandoverRequest) (*WorkOrderDetailResponse, error) {
		out, err := s.BusinessApplication().HandoverWorkOrder(ctx, principal(ctx), in.ID, in.Body)
		return &WorkOrderDetailResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_drafts_id_semantic_diff", "/drafts/{id}/semantic-diff", "read"), func(ctx context.Context, in *SemanticRequest) (*SemanticResponse, error) {
		out, err := s.BusinessApplication().SemanticDifference(ctx, principal(ctx), in.ID, in.Body)
		return &SemanticResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_drafts_id_impact", "/drafts/{id}/impact", "read"), func(ctx context.Context, in *ImpactRequest) (*ImpactResponse, error) {
		out, err := s.BusinessApplication().Impact(ctx, principal(ctx), in.ID, in.Body)
		return &ImpactResponse{Body: out}, contractError(err)
	})
	huma.Register(api, get("get_device_protocols", "/device-protocols"), func(ctx context.Context, _ *struct{}) (*ProtocolMetadataResponse, error) {
		out, err := s.BusinessApplication().DeviceProtocols(ctx, principal(ctx))
		return &ProtocolMetadataResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_device_configurations_validate", "/device-configurations/validate", "read"), func(ctx context.Context, in *ValidateDeviceConfigurationRequest) (*ValidateDeviceConfigurationResponse, error) {
		out, err := s.BusinessApplication().ValidateDeviceConfiguration(ctx, principal(ctx), in.Body)
		return &ValidateDeviceConfigurationResponse{Body: out}, contractError(err)
	})
	huma.Register(api, get("get_devices_id_configuration", "/devices/{id}/configuration"), func(ctx context.Context, in *BusinessIDRequest) (*DeviceConfigurationResponse, error) {
		out, err := s.BusinessApplication().DeviceConfiguration(ctx, principal(ctx), in.ID)
		return &DeviceConfigurationResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_device_configurations", "/device-configurations", "register"), func(ctx context.Context, in *SaveDeviceConfigurationRequest) (*DeviceConfigurationResponse, error) {
		out, err := s.BusinessApplication().SaveDeviceConfiguration(ctx, principal(ctx), in.Body)
		return &DeviceConfigurationResponse{Body: out}, contractError(err)
	})
	huma.Register(api, get("get_connector_configurations", "/connector-configurations"), func(ctx context.Context, _ *struct{}) (*ConnectorConfigurationsResponse, error) {
		out, err := s.BusinessApplication().ConnectorConfigurations(ctx, principal(ctx))
		return &ConnectorConfigurationsResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_connector_configurations", "/connector-configurations", "register"), func(ctx context.Context, in *SaveConnectorConfigurationRequest) (*ConnectorConfigurationResponse, error) {
		out, err := s.BusinessApplication().SaveConnectorConfiguration(ctx, principal(ctx), in.Body)
		return &ConnectorConfigurationResponse{Body: out}, contractError(err)
	})
	huma.Register(api, get("get_scene_templates", "/scene-templates"), func(ctx context.Context, _ *struct{}) (*TemplatesResponse, error) {
		out, err := s.BusinessApplication().Templates(ctx, principal(ctx))
		return &TemplatesResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_template_batches", "/template-batches", "draft"), func(ctx context.Context, in *PrepareTemplateBatchRequest) (*TemplateBatchResponse, error) {
		out, err := s.BusinessApplication().PrepareTemplateBatch(ctx, principal(ctx), in.Body)
		return &TemplateBatchResponse{Body: out}, contractError(err)
	})
	huma.Register(api, get("get_template_batches_id", "/template-batches/{id}"), func(ctx context.Context, in *BusinessIDRequest) (*TemplateBatchResponse, error) {
		out, err := s.BusinessApplication().TemplateBatch(ctx, principal(ctx), in.ID)
		return &TemplateBatchResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_template_batches_id_retry", "/template-batches/{id}/retry", "draft"), func(ctx context.Context, in *RetryTemplateBatchRequest) (*TemplateBatchResponse, error) {
		out, err := s.BusinessApplication().RetryTemplateBatch(ctx, principal(ctx), in.ID, in.Body)
		return &TemplateBatchResponse{Body: out}, contractError(err)
	})
}

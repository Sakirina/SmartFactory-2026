package api

import (
	"context"
	"net/http"
	"strings"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/pkg/model"
	"github.com/danielgtaylor/huma/v2"
)

type WorkloadIdentitiesResponse struct{ Body []model.WorkloadIdentity }
type WorkloadIdentityResponse struct{ Body model.WorkloadIdentity }
type SaveWorkloadIdentityRequest struct {
	Body application.SaveWorkloadIdentityInput
}
type RotateWorkloadCredentialRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body application.RotateWorkloadCredentialInput
}
type ConfigurationReportsResponse struct{ Body []model.ConfigurationReport }
type ConfigurationRuntimeResponse struct {
	Body []configcenter.RuntimeConfiguration
}
type ConfigurationReportsRequest struct {
	Authorization string `header:"Authorization"`
}

func (s *Server) NodeIdentities() *nodeidentity.Service {
	if s.Config != nil && s.Config.Workloads != nil {
		return s.Config.Workloads
	}
	return &nodeidentity.Service{Store: s.Store, Cipher: s.Identity}
}

func (s *Server) NodeConfigurationApplication() *application.NodeConfiguration {
	return &application.NodeConfiguration{Store: s.Store, Identity: s.Identity, Nodes: s.NodeIdentities(), Config: s.Config}
}

func NodeConfigurationContractTypes() []any {
	return []any{model.WorkloadIdentity{}, model.ConfigurationReference{}, model.ConfigurationEnvelope{}, model.ConfigurationVersion{}, model.ConfigurationReport{}, model.CredentialResolution{}, model.ConfigurationMetadata{}, model.ReleaseConfigurationTarget{}, model.ReleaseConfigurationTargetProof{}, model.ConnectorRegistration{}, model.ConnectorCredentialRequest{}, model.CredentialPayload{}, application.SaveWorkloadIdentityInput{}, application.RotateWorkloadCredentialInput{}, configcenter.RuntimeConfiguration{}}
}

func (s *Server) registerNodeConfigurationRoutes(api huma.API, operation func(string, string, string) huma.Operation) {
	runtime := operation("get_configuration_runtime", "/configuration-runtime", "read")
	runtime.Method = http.MethodGet
	huma.Register(api, runtime, func(ctx context.Context, _ *struct{}) (*ConfigurationRuntimeResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		if err := s.Identity.Permit(ctx, p, "read", s.NodeID); err != nil {
			return nil, contractError(err)
		}
		out, err := s.Config.RuntimeSnapshot(ctx)
		return &ConfigurationRuntimeResponse{Body: out}, contractError(err)
	})
	principal := func(ctx context.Context) identity.Principal {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		return p
	}
	get := operation("get_workload_identities", "/workload-identities", "read")
	get.Method = http.MethodGet
	huma.Register(api, get, func(ctx context.Context, _ *struct{}) (*WorkloadIdentitiesResponse, error) {
		out, err := s.NodeConfigurationApplication().Identities(ctx, principal(ctx))
		return &WorkloadIdentitiesResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_workload_identities", "/workload-identities", "identity"), func(ctx context.Context, in *SaveWorkloadIdentityRequest) (*WorkloadIdentityResponse, error) {
		out, err := s.NodeConfigurationApplication().SaveIdentity(ctx, principal(ctx), in.Body)
		return &WorkloadIdentityResponse{Body: out}, contractError(err)
	})
	huma.Register(api, operation("post_workload_identities_id_rotate", "/workload-identities/{id}/rotate", "identity"), func(ctx context.Context, in *RotateWorkloadCredentialRequest) (*WorkloadIdentityResponse, error) {
		out, err := s.NodeConfigurationApplication().RotateCredential(ctx, principal(ctx), in.ID, in.Body)
		return &WorkloadIdentityResponse{Body: out}, contractError(err)
	})
	reports := operation("get_configuration_reports", "/configuration-reports", "read")
	reports.Method = http.MethodGet
	huma.Register(api, reports, func(ctx context.Context, in *ConfigurationReportsRequest) (*ConfigurationReportsResponse, error) {
		if s.Mode == "cloud" && s.ConfigURL != "" {
			out, err := s.remoteConfigurationReports(ctx, strings.TrimPrefix(in.Authorization, "Bearer "))
			return &ConfigurationReportsResponse{Body: out}, contractError(err)
		}
		if s.NodeIdentities().AuthorityURL != "" {
			out, err := s.Config.PublicReports(ctx, strings.TrimPrefix(in.Authorization, "Bearer "))
			return &ConfigurationReportsResponse{Body: out}, contractError(err)
		}
		out, err := s.NodeConfigurationApplication().Reports(ctx, principal(ctx))
		return &ConfigurationReportsResponse{Body: out}, contractError(err)
	})
}

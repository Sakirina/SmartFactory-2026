package api

import (
	"context"
	"net/http"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/pkg/model"
	"github.com/danielgtaylor/huma/v2"
)

type InvestigationIDRequest struct {
	ID string `path:"id" minLength:"1" required:"true"`
}
type InvestigationEvidenceRequest struct {
	ID         string `path:"id" minLength:"1" required:"true"`
	EvidenceID string `path:"evidence_id" minLength:"1" required:"true"`
}
type InvestigationListRequest struct {
	After string `query:"after"`
	Limit int    `query:"limit" default:"20" minimum:"1" maximum:"100"`
}
type InvestigationResponse struct{ Body model.Investigation }
type InvestigationListResponse struct{ Body model.InvestigationList }
type InvestigationEvidenceResponse struct{ Body model.InvestigationEvidence }
type InvestigationDeleteResponse struct {
	Body struct {
		Deleted bool `json:"deleted"`
	}
}

func InvestigationContractTypes() []any {
	return []any{model.AssistantRequest{}, model.AssistantMessage{}, model.AssistantEvent{}, model.Investigation{}, model.InvestigationMessage{}, model.InvestigationEvidence{}, model.EvidenceReference{}, model.EvidenceResource{}, model.InvestigationList{}, model.AIDocumentScope{}, model.AIDocumentPage{}}
}

func (s *Server) InvestigationApplication() *application.Investigations {
	return &application.Investigations{Store: s.Store, Identity: s.Identity, Definitions: s.DefinitionApplication(), Control: s.Control, History: s.HistoryApplication(), Business: s.BusinessApplication()}
}

func (s *Server) registerInvestigationRoutes(api huma.API, operation func(string, string, string) huma.Operation) {
	get := func(id, path string) huma.Operation {
		op := operation(id, path, "read")
		op.Method = http.MethodGet
		return op
	}
	principal := func(ctx context.Context) identity.Principal {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		return p
	}
	huma.Register(api, get("get_investigations", "/investigations"), func(ctx context.Context, in *InvestigationListRequest) (*InvestigationListResponse, error) {
		out, err := s.InvestigationApplication().List(ctx, principal(ctx), in.After, in.Limit)
		return &InvestigationListResponse{Body: out}, contractError(err)
	})
	huma.Register(api, get("get_investigations_id", "/investigations/{id}"), func(ctx context.Context, in *InvestigationIDRequest) (*InvestigationResponse, error) {
		out, err := s.InvestigationApplication().Get(ctx, principal(ctx), in.ID)
		return &InvestigationResponse{Body: out}, contractError(err)
	})
	huma.Register(api, get("get_investigations_id_evidence_evidence_id", "/investigations/{id}/evidence/{evidence_id}"), func(ctx context.Context, in *InvestigationEvidenceRequest) (*InvestigationEvidenceResponse, error) {
		out, err := s.InvestigationApplication().Evidence(ctx, principal(ctx), in.ID, in.EvidenceID)
		return &InvestigationEvidenceResponse{Body: out}, contractError(err)
	})
	del := operation("delete_investigations_id", "/investigations/{id}", "read")
	del.Method = http.MethodDelete
	huma.Register(api, del, func(ctx context.Context, in *InvestigationIDRequest) (*InvestigationDeleteResponse, error) {
		err := s.InvestigationApplication().Delete(ctx, principal(ctx), in.ID)
		out := &InvestigationDeleteResponse{}
		out.Body.Deleted = err == nil
		return out, contractError(err)
	})
}

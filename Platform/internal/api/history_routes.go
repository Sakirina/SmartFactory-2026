package api

import (
	"context"
	"net/http"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/identity"
	"github.com/danielgtaylor/huma/v2"
)

type HistoryCreateRequest struct{ Body historymodel.Request }
type HistoryIDRequest struct {
	ID string `path:"id" minLength:"1"`
}
type HistoryListRequest struct {
	After string `query:"after"`
	Limit int    `query:"limit" default:"100" minimum:"1" maximum:"500"`
}
type HistoryStepsRequest struct {
	ID    string `path:"id" minLength:"1"`
	After int    `query:"after" default:"0" minimum:"0"`
	Limit int    `query:"limit" default:"100" minimum:"1" maximum:"500"`
}
type HistoryRunResponse struct{ Body historymodel.Run }
type HistoryListResponse struct{ Body historymodel.RunList }
type HistorySnapshotResponse struct{ Body historymodel.Snapshot }
type HistoryStepsResponse struct{ Body historymodel.StepList }
type ShadowCreateRequest struct{ Body historymodel.ShadowRequest }
type ShadowStopRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body historymodel.CandidateAction
}
type ShadowResponse struct{ Body historymodel.Candidate }
type ShadowsResponse struct{ Body historymodel.CandidateList }

func HistoryContractTypes() []any {
	return []any{historymodel.Request{}, historymodel.Run{}, historymodel.Snapshot{}, historymodel.Step{}, historymodel.RunList{}, historymodel.StepList{}, historymodel.ShadowRequest{}, historymodel.Candidate{}, historymodel.CandidateList{}, historymodel.CandidateAction{}}
}
func (s *Server) HistoryApplication() *application.History {
	return &application.History{Store: s.Store, Identity: s.Identity, Engine: s.Engine}
}

func (s *Server) registerHistoryRoutes(api huma.API, operation func(string, string, string) huma.Operation) {
	create := operation("post_analysis_runs", "/analysis-runs", "draft")
	create.DefaultStatus = http.StatusAccepted
	huma.Register(api, create, func(ctx context.Context, in *HistoryCreateRequest) (*HistoryRunResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		run, err := s.HistoryApplication().Create(ctx, p, in.Body)
		return &HistoryRunResponse{Body: run}, contractError(err)
	})
	list := operation("get_analysis_runs", "/analysis-runs", "read")
	list.Method = http.MethodGet
	huma.Register(api, list, func(ctx context.Context, in *HistoryListRequest) (*HistoryListResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		result, err := s.HistoryApplication().List(ctx, p, in.After, in.Limit)
		return &HistoryListResponse{Body: result}, contractError(err)
	})
	detail := operation("get_analysis_runs_id", "/analysis-runs/{id}", "read")
	detail.Method = http.MethodGet
	huma.Register(api, detail, func(ctx context.Context, in *HistoryIDRequest) (*HistoryRunResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		result, err := s.HistoryApplication().Get(ctx, p, in.ID)
		return &HistoryRunResponse{Body: result}, contractError(err)
	})
	snapshot := operation("get_analysis_runs_id_snapshot", "/analysis-runs/{id}/snapshot", "read")
	snapshot.Method = http.MethodGet
	huma.Register(api, snapshot, func(ctx context.Context, in *HistoryIDRequest) (*HistorySnapshotResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		result, err := s.HistoryApplication().Snapshot(ctx, p, in.ID)
		return &HistorySnapshotResponse{Body: result}, contractError(err)
	})
	steps := operation("get_analysis_runs_id_steps", "/analysis-runs/{id}/steps", "read")
	steps.Method = http.MethodGet
	huma.Register(api, steps, func(ctx context.Context, in *HistoryStepsRequest) (*HistoryStepsResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		result, err := s.HistoryApplication().Steps(ctx, p, in.ID, in.After, in.Limit)
		return &HistoryStepsResponse{Body: result}, contractError(err)
	})
	enable := operation("post_shadow_candidates", "/shadow-candidates", "draft")
	huma.Register(api, enable, func(ctx context.Context, in *ShadowCreateRequest) (*ShadowResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		result, err := s.HistoryApplication().EnableShadow(ctx, p, in.Body)
		return &ShadowResponse{Body: result}, contractError(err)
	})
	shadows := operation("get_shadow_candidates", "/shadow-candidates", "read")
	shadows.Method = http.MethodGet
	huma.Register(api, shadows, func(ctx context.Context, _ *struct{}) (*ShadowsResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		result, err := s.HistoryApplication().Shadows(ctx, p)
		return &ShadowsResponse{Body: result}, contractError(err)
	})
	shadow := operation("get_shadow_candidates_id", "/shadow-candidates/{id}", "read")
	shadow.Method = http.MethodGet
	huma.Register(api, shadow, func(ctx context.Context, in *HistoryIDRequest) (*ShadowResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		result, err := s.HistoryApplication().Shadow(ctx, p, in.ID)
		return &ShadowResponse{Body: result}, contractError(err)
	})
	stop := operation("post_shadow_candidates_id_stop", "/shadow-candidates/{id}/stop", "draft")
	huma.Register(api, stop, func(ctx context.Context, in *ShadowStopRequest) (*ShadowResponse, error) {
		p, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		result, err := s.HistoryApplication().DisableShadow(ctx, p, in.ID, in.Body.ExpectedVersion)
		return &ShadowResponse{Body: result}, contractError(err)
	})
}

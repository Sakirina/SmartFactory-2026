package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/pkg/model"
	"github.com/danielgtaylor/huma/v2"
)

type ReleasesResponse struct{ Body []model.Release }
type ReleaseResponse struct{ Body model.Release }
type CreateReleaseRequest struct {
	Body application.CreateReleaseInput
}
type ValidateReleaseRequest struct{ Body model.ReleaseManifest }
type ReleaseValidationResponse struct{ Body model.ReleaseValidation }
type ReleaseArtifactsResponse struct{ Body []model.ReleaseArtifact }
type ReleaseArtifactResponse struct{ Body model.ReleaseArtifact }
type RegisterReleaseArtifactRequest struct {
	Body application.RegisterReleaseArtifactInput
}
type ReleaseDeploymentsResponse struct{ Body []model.ReleaseDeployment }
type ReleaseDeploymentResponse struct{ Body model.ReleaseDeployment }
type CreateReleaseDeploymentRequest struct {
	Body application.CreateReleaseDeploymentInput
}
type ReleaseDeploymentActionRequest struct {
	ID   string `path:"id"`
	Body application.ReleaseDeploymentActionInput
}
type ReleaseRollbackRequest struct {
	ID   string `path:"id"`
	Body application.ReleaseRollbackInput
}
type ReleaseReportsResponse struct{ Body []model.ReleaseNodeReport }
type TemplateEvolutionResponse struct{ Body model.TemplateEvolution }
type TemplateEvolutionsResponse struct{ Body []model.TemplateEvolution }
type EvolveTemplateBatchRequest struct {
	ID   string `path:"id"`
	Body application.EvolveTemplateBatchInput
}

func (s *Server) ReleaseApplication() *application.Releases {
	v := &application.Releases{Store: s.Store, Business: s.BusinessApplication(), Nodes: s.NodeIdentities(), Config: s.Config, ArtifactRoot: s.ReleaseArtifactRoot, Mode: s.Mode}
	if s.ConfigURL != "" && s.Mode == "cloud" {
		v.ConfigurationMetadata = s.releaseConfigurationMetadata
	}
	return v
}
func ReleaseContractTypes() []any {
	return []any{model.ProgramBuild{}, model.ReleaseDependency{}, model.ReleaseComponent{}, model.ReleaseTemplateOrigin{}, model.ReleaseManifest{}, model.Release{}, model.ReleaseArtifact{}, model.ReleaseIssue{}, model.ReleaseValidation{}, model.ReleaseRuntimeComponent{}, model.ReleaseRuntime{}, model.ReleaseNodeReport{}, model.ReleaseTarget{}, model.ReleaseBatch{}, model.ReleaseDeployment{}, model.ReleaseDesired{}, application.CreateReleaseInput{}, application.CreateReleaseDeploymentInput{}, application.ReleaseDeploymentActionInput{}, application.ReleaseRollbackInput{}, application.RegisterReleaseArtifactInput{}, model.TemplateEvolution{}, application.TemplateEvolutionTarget{}, application.EvolveTemplateBatchInput{}}
}

func (s *Server) registerReleaseRoutes(api huma.API, operation func(string, string, string) huma.Operation) {
	p := func(ctx context.Context) identity.Principal {
		v, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		return v
	}
	get := func(id, path string) huma.Operation {
		o := operation(id, path, "read")
		o.Method = http.MethodGet
		return o
	}
	huma.Register(api, get("get_releases", "/releases"), func(ctx context.Context, _ *struct{}) (*ReleasesResponse, error) {
		v, e := s.ReleaseApplication().List(ctx, p(ctx))
		return &ReleasesResponse{Body: v}, contractError(e)
	})
	huma.Register(api, operation("post_releases", "/releases", "publish"), func(ctx context.Context, in *CreateReleaseRequest) (*ReleaseResponse, error) {
		v, e := s.ReleaseApplication().Create(ctx, p(ctx), in.Body)
		return &ReleaseResponse{Body: v}, contractError(e)
	})
	huma.Register(api, operation("post_releases_validate", "/releases/validate", "publish"), func(ctx context.Context, in *ValidateReleaseRequest) (*ReleaseValidationResponse, error) {
		v, e := s.ReleaseApplication().Validate(ctx, p(ctx), in.Body)
		return &ReleaseValidationResponse{Body: v}, contractError(e)
	})
	huma.Register(api, get("get_releases_id", "/releases/{id}"), func(ctx context.Context, in *BusinessIDRequest) (*ReleaseResponse, error) {
		v, e := s.ReleaseApplication().Get(ctx, p(ctx), in.ID)
		return &ReleaseResponse{Body: v}, contractError(e)
	})
	huma.Register(api, get("get_release_artifacts", "/release-artifacts"), func(ctx context.Context, _ *struct{}) (*ReleaseArtifactsResponse, error) {
		v, e := s.ReleaseApplication().Artifacts(ctx, p(ctx))
		return &ReleaseArtifactsResponse{Body: v}, contractError(e)
	})
	huma.Register(api, operation("post_release_artifacts", "/release-artifacts", "publish"), func(ctx context.Context, in *RegisterReleaseArtifactRequest) (*ReleaseArtifactResponse, error) {
		v, e := s.ReleaseApplication().RegisterArtifact(ctx, p(ctx), in.Body)
		return &ReleaseArtifactResponse{Body: v}, contractError(e)
	})
	huma.Register(api, get("get_release_deployments", "/release-deployments"), func(ctx context.Context, _ *struct{}) (*ReleaseDeploymentsResponse, error) {
		v, e := s.ReleaseApplication().Deployments(ctx, p(ctx))
		return &ReleaseDeploymentsResponse{Body: v}, contractError(e)
	})
	huma.Register(api, operation("post_release_deployments", "/release-deployments", "publish"), func(ctx context.Context, in *CreateReleaseDeploymentRequest) (*ReleaseDeploymentResponse, error) {
		v, e := s.ReleaseApplication().CreateDeployment(ctx, p(ctx), in.Body)
		return &ReleaseDeploymentResponse{Body: v}, contractError(e)
	})
	huma.Register(api, get("get_release_deployments_id", "/release-deployments/{id}"), func(ctx context.Context, in *BusinessIDRequest) (*ReleaseDeploymentResponse, error) {
		v, e := s.ReleaseApplication().Deployment(ctx, p(ctx), in.ID)
		return &ReleaseDeploymentResponse{Body: v}, contractError(e)
	})
	huma.Register(api, operation("post_release_deployments_id_actions", "/release-deployments/{id}/actions", "publish"), func(ctx context.Context, in *ReleaseDeploymentActionRequest) (*ReleaseDeploymentResponse, error) {
		v, e := s.ReleaseApplication().Action(ctx, p(ctx), in.ID, in.Body)
		return &ReleaseDeploymentResponse{Body: v}, contractError(e)
	})
	huma.Register(api, operation("post_release_deployments_id_rollback", "/release-deployments/{id}/rollback", "publish"), func(ctx context.Context, in *ReleaseRollbackRequest) (*ReleaseDeploymentResponse, error) {
		v, e := s.ReleaseApplication().Rollback(ctx, p(ctx), in.ID, in.Body)
		return &ReleaseDeploymentResponse{Body: v}, contractError(e)
	})
	huma.Register(api, get("get_release_deployments_id_reports", "/release-deployments/{id}/reports"), func(ctx context.Context, in *BusinessIDRequest) (*ReleaseReportsResponse, error) {
		v, e := s.ReleaseApplication().Reports(ctx, p(ctx), in.ID)
		return &ReleaseReportsResponse{Body: v}, contractError(e)
	})
	huma.Register(api, operation("post_template_batches_id_evolve", "/template-batches/{id}/evolve", "publish"), func(ctx context.Context, in *EvolveTemplateBatchRequest) (*TemplateEvolutionResponse, error) {
		v, e := s.ReleaseApplication().EvolveTemplateBatch(ctx, p(ctx), in.ID, in.Body)
		return &TemplateEvolutionResponse{Body: v}, contractError(e)
	})
	huma.Register(api, get("get_template_batches_id_evolutions", "/template-batches/{id}/evolutions"), func(ctx context.Context, in *BusinessIDRequest) (*TemplateEvolutionsResponse, error) {
		v, e := s.ReleaseApplication().TemplateEvolutions(ctx, p(ctx), in.ID)
		return &TemplateEvolutionsResponse{Body: v}, contractError(e)
	})
}

func releaseTransport(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	return e == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}
func (s *Server) registerReleaseInternal(mux *http.ServeMux) {
	mux.HandleFunc("GET /internal/releases/runtime", func(w http.ResponseWriter, r *http.Request) {
		if !releaseTransport(r) || s.ServiceToken == "" || subtle.ConstantTimeCompare([]byte(bearer(r)), []byte(s.ServiceToken)) != 1 {
			fail(w, identity.ErrDenied)
			return
		}
		if s.ReleaseRuntime == nil {
			fail(w, APIError{404, "no release is active in this process"})
			return
		}
		runtime, err := s.ReleaseRuntime.Snapshot(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		configuration, err := s.Config.RuntimeSnapshot(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, map[string]any{"runtime": runtime, "configurations": configuration})
	})
	if s.Mode != "cloud" {
		return
	}
	auth := func(fn func(http.ResponseWriter, *http.Request, nodeidentity.Principal) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !releaseTransport(r) {
				fail(w, identity.ErrDenied)
				return
			}
			p, e := s.NodeIdentities().FromRequest(r)
			if e == nil {
				e = fn(w, r, p)
			}
			if e != nil {
				fail(w, e)
			}
		}
	}
	mux.HandleFunc("GET /internal/releases/desired", auth(func(w http.ResponseWriter, r *http.Request, p nodeidentity.Principal) error {
		v, e := s.ReleaseApplication().Desired(r.Context(), p)
		if e == nil {
			respond(w, 200, v)
		}
		return e
	}))
	mux.HandleFunc("POST /internal/releases/reports", auth(func(w http.ResponseWriter, r *http.Request, p nodeidentity.Principal) error {
		var in model.ReleaseNodeReport
		if e := decode(r, &in); e != nil {
			return e
		}
		v, e := s.ReleaseApplication().Report(r.Context(), p, in)
		if e == nil {
			respond(w, 200, v)
		}
		return e
	}))
	mux.HandleFunc("GET /internal/releases/artifacts/{sha256}", auth(func(w http.ResponseWriter, r *http.Request, p nodeidentity.Principal) error {
		path, e := s.ReleaseApplication().ArtifactForNode(r.Context(), p, r.PathValue("sha256"))
		if e != nil {
			if errors.Is(e, releasebundle.ErrArtifactIntegrity) {
				return APIError{422, e.Error()}
			}
			return e
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, path)
		return nil
	}))
	s.route(mux, "PUT /api/sf/v1/release-artifacts/{sha256}", "publish", func(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
		if _, e := s.ReleaseApplication().Artifacts(r.Context(), p); e != nil {
			return e
		}
		n, e := releasebundle.WriteArtifact(s.ReleaseArtifactRoot, r.PathValue("sha256"), http.MaxBytesReader(w, r.Body, releasebundle.MaxArtifactBytes))
		if e == nil {
			respond(w, 200, map[string]any{"sha256": r.PathValue("sha256"), "size": n})
		}
		return e
	})
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"github.com/danielgtaylor/huma/v2"
)

type SimulationOperationRequest struct {
	ID   string `path:"id" minLength:"1"`
	Body engine.SimulationRequest
}

type SimulationPayload struct {
	Legacy   *engine.Evaluation       `json:"-"`
	Sequence *engine.SimulationResult `json:"-"`
}

func (p SimulationPayload) MarshalJSON() ([]byte, error) {
	if p.Legacy != nil {
		return json.Marshal(p.Legacy)
	}
	return json.Marshal(p.Sequence)
}

func (SimulationPayload) Schema(registry huma.Registry) *huma.Schema {
	return &huma.Schema{OneOf: []*huma.Schema{
		registry.Schema(reflect.TypeFor[engine.Evaluation](), true, "Evaluation"),
		registry.Schema(reflect.TypeFor[engine.SimulationResult](), true, "SimulationResult"),
	}, Description: "A point request returns Evaluation; a points request returns SimulationResult with resolved inputs, clocks, state and snapshot identities."}
}

type SimulationResponse struct{ Body SimulationPayload }
type NodeCatalogResponse struct{ Body []model.NodeMetadata }
type PlanDiagnosticsRequest struct {
	ID string `path:"id" minLength:"1"`
}
type PlanDiagnosticsResponse struct{ Body []engine.PlanDiagnostic }

func RuleContractTypes() []any {
	return []any{engine.SimulationRequest{}, engine.SimulationResult{}, engine.PlanDiagnostic{}, model.NodeMetadata{}}
}

func (s *Server) registerRuleRoutes(api huma.API, operation func(string, string, string) huma.Operation) {
	simulation := operation("post_drafts_id_simulate", "/drafts/{id}/simulate", "draft")
	simulation.Description = "Compile the selected draft revision once. points executes up to 1000 inputs with independent entity state and at most 40000 history inputs, within 5000 ms. event_time order sorts by observed_ms, source_sequence, then id; provided preserves array order. An explicit history: [] selects an empty history; omission snapshots stored history. A legacy point body returns Evaluation."
	huma.Register(api, simulation, func(ctx context.Context, input *SimulationOperationRequest) (*SimulationResponse, error) {
		principal, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		draft, err := s.DefinitionApplication().LoadDraft(ctx, principal, application.DraftInput{ID: input.ID, ExpectedVersion: input.Body.ExpectedVersion}, "draft")
		if err != nil {
			return nil, contractError(err)
		}
		result, err := s.Engine.SimulateSequenceAuthorized(ctx, draft.Definition, input.Body, func(ctx context.Context, id string) error { return s.Identity.Permit(ctx, principal, "read", id) })
		if err != nil {
			return nil, contractError(err)
		}
		result.DraftRevision = draft.Version
		payload := SimulationPayload{Sequence: &result}
		if input.Body.Point != nil {
			payload = SimulationPayload{Legacy: &result.Results[0].Evaluation}
		}
		return &SimulationResponse{Body: payload}, nil
	})
	catalog := operation("get_rule_nodes", "/rule-nodes", "read")
	catalog.Method = http.MethodGet
	catalog.Description = "The same node catalog supplies compilation, parameter schemas, port types, definition applicability, defaults, units and editor help."
	huma.Register(api, catalog, func(context.Context, *struct{}) (*NodeCatalogResponse, error) {
		return &NodeCatalogResponse{Body: model.NodeCatalog()}, nil
	})
	diagnostics := operation("get_definitions_id_plans", "/definitions/{id}/plans", "read")
	diagnostics.Method = http.MethodGet
	huma.Register(api, diagnostics, func(ctx context.Context, input *PlanDiagnosticsRequest) (*PlanDiagnosticsResponse, error) {
		principal, _ := ctx.Value(definitionPrincipalKey{}).(identity.Principal)
		documents, err := s.Store.Versions(ctx, "definition", input.ID)
		if err != nil {
			return nil, contractError(err)
		}
		if len(documents) == 0 {
			return nil, contractError(store.ErrNotFound)
		}
		for _, document := range documents {
			definition, err := store.Decode[model.Definition](document)
			if err != nil {
				return nil, contractError(err)
			}
			if err := s.authorizePlan(ctx, principal, definition, map[string]bool{}); err != nil {
				return nil, contractError(err)
			}
		}
		result, err := s.Engine.PlanDiagnostics(ctx, input.ID)
		return &PlanDiagnosticsResponse{Body: result}, contractError(err)
	})
}

func (s *Server) authorizePlan(ctx context.Context, principal identity.Principal, definition model.Definition, visited map[string]bool) error {
	if visited[definition.ID] {
		return nil
	}
	visited[definition.ID] = true
	group := definition.GroupID
	if group == "" {
		group = definition.ID
	}
	resources := append([]string{group}, definition.Selector.DeviceIDs...)
	if definition.Selector.AssetID != "" {
		resources = append(resources, definition.Selector.AssetID)
	}
	for _, condition := range definition.Policy.Conditions {
		resources = append(resources, condition.DeviceID)
	}
	for _, step := range append(append([]model.Step{}, definition.Policy.Steps...), definition.Policy.Degraded...) {
		resources = append(resources, step.DeviceID)
	}
	for _, resource := range resources {
		if resource == "" {
			return identity.ErrDenied
		}
		if err := s.Identity.Permit(ctx, principal, "read", resource); err != nil {
			return err
		}
	}
	for _, id := range definition.Dependencies {
		dependency, err := s.Engine.Published(ctx, id, definition.EffectiveMS)
		if err != nil {
			// Missing references still require a grant to that identifier before
			// their diagnostic is disclosed.
			if err == store.ErrNotFound {
				if err := s.Identity.Permit(ctx, principal, "read", id); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if err := s.authorizePlan(ctx, principal, dependency, visited); err != nil {
			return err
		}
	}
	return nil
}

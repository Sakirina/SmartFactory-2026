package api

import (
	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/pkg/contracts"
	"competition2026/product/platform/pkg/model"
	"net/http"
)

var publicContracts = func() *contracts.Registry {
	registry := contracts.New()
	for _, value := range []any{application.SaveDraftInput{}, DraftRevisionRequest{}, ContractError{}, CompatibilityStatus{}} {
		registry.Add(value)
	}
	for _, value := range TaskContractTypes() {
		registry.Add(value)
	}
	for _, value := range RuleContractTypes() {
		registry.Add(value)
	}
	for _, value := range BusinessContractTypes() {
		registry.Add(value)
	}
	for _, value := range InvestigationContractTypes() {
		registry.Add(value)
	}
	for _, value := range QueryContractTypes() {
		registry.Add(value)
	}
	return registry
}()

func (s *Server) contracts(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if name := r.PathValue("name"); name != "" {
		if name == "operations" {
			respond(w, 200, DefinitionOpenAPI())
			return nil
		}
		schema, ok := publicContracts.Schema(name)
		if !ok {
			return APIError{404, "unknown contract"}
		}
		respond(w, 200, schema)
		return nil
	}
	respond(w, 200, map[string]any{"contract_version": model.ContractVersion, "schemas": publicContracts.Names(), "node_kinds": model.NodeKinds(), "schema_path": "/api/sf/v1/contracts/{name}"})
	return nil
}

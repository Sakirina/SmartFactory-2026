package api

import (
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/pkg/contracts"
	"competition2026/product/platform/pkg/model"
	"net/http"
)

var publicContracts = contracts.New()

func (s *Server) contracts(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if name := r.PathValue("name"); name != "" {
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

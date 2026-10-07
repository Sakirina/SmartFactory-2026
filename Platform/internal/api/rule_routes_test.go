package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func saveSimulationDraft(t *testing.T, s *Server, token string) model.Draft {
	t.Helper()
	draft := model.Draft{ID: "simulation", Definition: model.Definition{ID: "calculation", Name: "Calculation", Kind: "analysis", SchemaVersion: model.ContractVersion, GroupID: "a", Selector: model.Selector{DeviceIDs: []string{"device-a"}, Keys: []string{"count"}}, Nodes: []model.Node{{ID: "counter", Type: "counter", Params: map[string]any{"mode": "delta"}}}, Outputs: []model.Output{{NodeID: "counter", Key: "count", Type: "integer", Unit: "piece"}}}}
	w := call(s, token, http.MethodPost, "/api/sf/v1/drafts", map[string]any{"draft": draft, "expected_version": 0})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := store.DecodeJSON(w.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	return draft
}

func TestContinuousSimulationAPIRevisionPrecisionPermissionsAndCompatibility(t *testing.T) {
	s, token := scopedServer(t, false)
	draft := saveSimulationDraft(t, s, token)
	point := model.Observation{ID: "one", DeviceID: "device-a", Key: "count", ObservedMS: s.Store.Now().UnixMilli(), Value: json.Number("9007199254740993"), Quality: "GOOD", Unit: "piece"}
	before := rulecore.SnapshotStatistics()
	w := call(s, token, http.MethodPost, "/api/sf/v1/drafts/simulation/simulate", map[string]any{"expected_version": draft.Version, "points": []model.Observation{point}, "history": []model.Observation{}})
	var result engine.SimulationResult
	if w.Code != 200 || store.DecodeJSON(w.Body.Bytes(), &result) != nil || result.DraftRevision != draft.Version || len(result.Results) != 1 || result.FinalState["device-a"]["counter"].Count != 9007199254740993 || result.PlanID == "" || result.HistorySHA256 == "" {
		t.Fatalf("sequence response: %d %s", w.Code, w.Body.String())
	}
	after := rulecore.SnapshotStatistics()
	if after.Compilations-before.Compilations != 1 {
		t.Fatal("API compiled per point", before, after)
	}
	if !strings.Contains(w.Body.String(), `9007199254740993`) {
		t.Fatal("integer precision lost")
	}
	w = call(s, token, http.MethodPost, "/api/sf/v1/drafts/simulation/simulate", map[string]any{"expected_version": draft.Version - 1, "points": []model.Observation{point}, "history": []model.Observation{}})
	if w.Code != 409 {
		t.Fatal("stale draft accepted", w.Code, w.Body.String())
	}
	w = call(s, token, http.MethodPost, "/api/sf/v1/drafts/simulation/simulate", map[string]any{"point": point})
	var legacy engine.Evaluation
	if w.Code != 200 || store.DecodeJSON(w.Body.Bytes(), &legacy) != nil || legacy.DefinitionID != draft.Definition.ID || legacy.States["counter"].Count != 9007199254740993 || strings.Contains(w.Body.String(), `"results"`) {
		t.Fatal("single-point compatibility", w.Code, w.Body.String())
	}
	for _, reference := range []string{"point", "history", "initial_state", "asset_parent"} {
		t.Run(reference, func(t *testing.T) {
			foreign := point
			foreign.DeviceID = "device-b"
			body := map[string]any{"points": []model.Observation{point}, "history": []model.Observation{}}
			switch reference {
			case "point":
				body["points"] = []model.Observation{foreign}
			case "history":
				body["history"] = []model.Observation{foreign}
			case "initial_state":
				body["initial_state"] = map[string]any{"device-b": map[string]any{}}
			case "asset_parent":
				body["asset_versions"] = []model.AssetVersion{{ID: "device-a", Version: 1, EffectiveMS: 1, ParentID: "b"}}
			}
			w := call(s, token, http.MethodPost, "/api/sf/v1/drafts/simulation/simulate", body)
			if w.Code != 403 {
				t.Fatal("foreign reference accepted", w.Code, w.Body.String())
			}
		})
	}
	for _, kind := range []string{"engine_state", "recompute_state", "active_alarm", "alarm", "execution", "compiled_plan"} {
		docs, err := s.Store.List(context.Background(), kind)
		if err != nil || len(docs) != 0 {
			t.Fatal("API simulation wrote state", kind, docs, err)
		}
	}
}

func TestNodeCatalogAndPlanDiagnosticsEnforceResourceAccess(t *testing.T) {
	s, token := scopedServer(t, false)
	draft := saveSimulationDraft(t, s, token)
	w := call(s, token, http.MethodGet, "/api/sf/v1/rule-nodes", nil)
	var catalog []model.NodeMetadata
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog) != 12 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, node := range catalog {
		if node.Description == "" || node.ParameterSchema == nil || len(node.Kinds) == 0 || len(node.Outputs) == 0 {
			t.Fatal("incomplete node metadata", node)
		}
	}
	d := draft.Definition
	d.Status = "published"
	d.Version = 1
	if _, err := s.Store.Put(context.Background(), "definition", d.ID, 0, d); err != nil {
		t.Fatal(err)
	}
	if err := s.Engine.PrepareDefinition(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	w = call(s, token, http.MethodGet, "/api/sf/v1/definitions/calculation/plans", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"prepared"`) || !strings.Contains(w.Body.String(), `"plan_sha256"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	d.ID = "invalid"
	d.Nodes = nil
	d.Outputs = nil
	if _, err := s.Store.Put(context.Background(), "definition", d.ID, 0, d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.PreparePublishedPlans(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = call(s, token, http.MethodGet, "/api/sf/v1/definitions/invalid/plans", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"isolated"`) || !strings.Contains(w.Body.String(), `"code":"definition_compilation"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	d.ID = "foreign"
	d.Selector.DeviceIDs = []string{"device-b"}
	if _, err := s.Store.Put(context.Background(), "definition", d.ID, 0, d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Engine.PreparePublishedPlans(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = call(s, token, http.MethodGet, "/api/sf/v1/definitions/foreign/plans", nil)
	if w.Code != 403 {
		t.Fatal("plan diagnostic disclosed foreign selector", w.Code, w.Body.String())
	}
}

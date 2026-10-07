package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func controlServer(t *testing.T) (*Server, string, model.Definition) {
	t.Helper()
	s, token := scopedServer(t, false)
	s.Control = &control.Service{Store: s.Store, Identity: s.Identity, Definitions: s.Engine, NodeID: "cloud"}
	policy := s.Store.Policy()
	policy.Confirmations.Leaders = 0
	s.Store.SetPolicy(policy)
	d := model.Definition{ID: "control-plan", Kind: "strategy", GroupID: "a", Status: "published", Version: 1, Policy: model.Policy{RiskCategory: "business", RiskLevel: 1, EdgeIDs: []string{"edge-a"}, Steps: []model.Step{{ID: "step", DeviceID: "device-a", EdgeID: "edge-a", Action: "actuate"}}}}
	if _, err := s.Store.Put(context.Background(), "definition", d.ID, 0, d); err != nil {
		t.Fatal(err)
	}
	return s, token, d
}
func TestControlHTTPCloudPendingAndVersionContract(t *testing.T) {
	s, token, d := controlServer(t)
	w := call(s, token, "POST", "/api/sf/v1/executions", map[string]any{"definition_id": d.ID, "downlink_id": "cloud-pending", "params": map[string]string{}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var req model.Execution
	if err := json.Unmarshal(w.Body.Bytes(), &req); err != nil {
		t.Fatal(err)
	}
	w = call(s, token, "POST", "/api/sf/v1/executions/"+req.DownlinkID+"/cancel", model.ExecutionAction{ExpectedVersion: req.Version, Reason: "云端请求取消", OperationID: "cloud-cancel"})
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	source := int64(0)
	input := model.ExecutionAction{ExpectedVersion: req.Version, ExpectedSourceVersion: &source, Reason: "云端请求取消", OperationID: "cloud-cancel"}
	w = call(s, token, "POST", "/api/sf/v1/executions/"+req.DownlinkID+"/cancel", input)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var op model.ControlOperation
	if err := json.Unmarshal(w.Body.Bytes(), &op); err != nil || op.Status != "pending" {
		t.Fatal(op, err)
	}
	current, err := s.Control.Get(context.Background(), req.DownlinkID)
	if err != nil || current.Status != req.Status || current.Version != req.Version {
		t.Fatal(current, err)
	}
	w = call(s, token, "GET", "/api/sf/v1/executions/"+req.DownlinkID, nil)
	var detail model.ExecutionDetail
	if err = json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 || detail.SourceVersion != 0 || len(detail.Operations) != 1 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	input.ExpectedVersion++
	w = call(s, token, "POST", "/api/sf/v1/executions/"+req.DownlinkID+"/cancel", input)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Log("actual HTTP202 operation pending; execution revision unchanged; detail exposes source version; stale same operation identity returns409")
}
func TestControlHTTPReadsHistoricalResourcesAndRechecksPrincipal(t *testing.T) {
	s, token, d := controlServer(t)
	ctx := context.Background()
	d.Policy.Steps[0].DeviceID = "device-b"
	d.Version = 2
	if _, err := s.Store.Put(ctx, "definition", d.ID, 1, d); err != nil {
		t.Fatal(err)
	}
	req := model.Execution{DownlinkID: "old-execution", DefinitionID: d.ID, DefinitionVersion: 2, Status: "result_unknown", Version: 1, CreatedMS: time.Now().UnixMilli(), Snapshot: []model.Observation{{DeviceID: "device-b", Key: "private"}}}
	if _, err := s.Store.Put(ctx, "execution", req.DownlinkID, 0, req); err != nil {
		t.Fatal(err)
	}
	d.Version = 3
	d.Policy.Steps[0].DeviceID = "device-a"
	if _, err := s.Store.Put(ctx, "definition", d.ID, 2, d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Put(ctx, "control_operation", "old-op", 0, model.ControlOperation{ID: "old-op", ExecutionID: req.DownlinkID, Result: &req, Version: 1}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/sf/v1/executions/old-execution", "/api/sf/v1/control-operations/old-op"} {
		w := call(s, token, "GET", path, nil)
		if w.Code != 403 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	// Application calls reject the same inaccessible historical resources.
	principal, err := s.Identity.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Control.Detail(ctx, principal, req.DownlinkID); !errors.Is(err, identity.ErrDenied) {
		t.Fatal(err)
	}
	doc, _ := s.Store.Get(ctx, "user", "user")
	user, _ := store.Decode[model.User](doc)
	user.Active = false
	if _, err = s.Store.Put(ctx, "user", user.ID, doc.Version, user); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Control.Operation(ctx, principal, "old-op"); !errors.Is(err, identity.ErrDenied) {
		t.Fatal(err)
	}
}

package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"competition2026/product/platform/internal/cloudsync"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/compatibility"
	"competition2026/product/platform/pkg/model"
)

func TestHTTPImportedControlEvidenceAndCurrentResourcePermission(t *testing.T) {
	s, token, definition := controlServer(t)
	ctx := context.Background()
	now := s.Store.Now().UnixMilli()
	step := definition.Policy.Steps[0]
	step.Params = map[string]string{}
	evidence := model.CommandEvidence{ID: "imported-evidence", ExecutionID: "imported-execution", CommandID: "imported-execution:step", StepID: step.ID, DeviceID: step.DeviceID, PayloadHash: store.Hash(step), ContentHash: "original-result", Source: "datatransfer.command_journal:edge-a", Status: "SUCCESS", Trusted: true, ObservedMS: now - 10, CollectedMS: now, TraceID: "0123456789abcdef0123456789abcdef"}
	req := model.Execution{DownlinkID: evidence.ExecutionID, DefinitionID: definition.ID, DefinitionVersion: definition.Version, Version: 7, Status: "completed", CreatedMS: now - 100, Steps: []model.StepResult{{StepID: step.ID, CommandID: evidence.CommandID, Status: evidence.Status, StartedMS: now - 50, FinishedMS: now - 10, EvidenceID: evidence.ID}}}
	envelope := model.ExecutionReceipt{Execution: req, ReceiptSource: &model.ExecutionReceiptSource{SchemaVersion: 1, NodeID: "edge-a", Version: req.Version, RecordedMS: now}, CommandEvidence: []model.CommandEvidence{evidence}}
	raw, _ := json.Marshal(envelope)
	encryptionPublic, err := s.Identity.EncryptionPublicKey()
	if err != nil {
		t.Fatal(err)
	}
	registration := cloudsync.Registration{AuditPublicKey: base64.StdEncoding.EncodeToString(s.Store.SignKey.Public().(ed25519.PublicKey)), EncryptionPublicKey: encryptionPublic}
	response, err := (&cloudsync.Server{Store: s.Store, Identity: s.Identity}).Exchange(ctx, "edge-a", registration, cloudsync.Request{Compatibility: compatibility.CurrentPeer("edge"), Deliveries: []store.Delivery{{ID: fmt.Sprintf("receipt:%s:%d", req.DownlinkID, req.Version), Kind: "cloud_receipt", Payload: raw}}})
	if err != nil || len(response.Failures) != 0 || len(response.Committed) != 1 {
		t.Fatal(response, err)
	}
	w := call(s, token, "GET", "/api/sf/v1/executions/"+req.DownlinkID, nil)
	var detail model.ExecutionDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 || len(detail.Evidence) != 1 || store.Hash(detail.Evidence[0]) != store.Hash(evidence) {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	found := false
	for _, item := range detail.Timeline {
		found = found || item.Kind == "feedback" && item.Evidence != nil && item.Evidence.ID == evidence.ID && item.Source == evidence.Source && item.TraceID == evidence.TraceID
	}
	if !found {
		t.Fatal("imported feedback absent from HTTP timeline", detail)
	}
	if _, err := s.Store.Put(ctx, "control_operation", "imported-result", 0, model.ControlOperation{ID: "imported-result", ExecutionID: req.DownlinkID, Result: &req, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if w := call(s, token, "GET", "/api/sf/v1/control-operations/imported-result", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	doc, err := s.Store.Get(ctx, "entity", step.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	device, _ := store.Decode[model.Entity](doc)
	device.ParentID, device.Version = "b", doc.Version+1
	if _, err := s.Store.Put(ctx, "entity", device.ID, doc.Version, device); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/sf/v1/executions/" + req.DownlinkID, "/api/sf/v1/control-operations/imported-result"} {
		if w := call(s, token, "GET", path, nil); w.Code != 403 {
			t.Fatal("current moved resource leaked imported evidence", path, w.Code, w.Body.String())
		}
	}
	t.Log("cloud exchange-imported evidence and feedback trace returned by actual HTTP; moving original device outside current account scope denies detail and operation result")
}

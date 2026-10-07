package cloudsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func receiptExample(t *testing.T, database *store.Store) (model.ExecutionReceipt, model.Definition) {
	t.Helper()
	now := database.Now().UnixMilli()
	step := model.Step{ID: "first", DeviceID: "counter", EdgeID: "edge-a", Action: "write", Params: map[string]string{"value": "42"}}
	definition := model.Definition{ID: "control-receipt-plan", Kind: "strategy", Version: 1, Status: "published", GroupID: "factory", Policy: model.Policy{EdgeIDs: []string{"edge-a"}, Steps: []model.Step{step}}}
	if _, err := database.Put(context.Background(), "definition", definition.ID, 0, definition); err != nil {
		t.Fatal(err)
	}
	item := model.CommandEvidence{ID: "original-command-feedback", ExecutionID: "生产线:回执", CommandID: "生产线:回执:first", StepID: step.ID, DeviceID: step.DeviceID, PayloadHash: store.Hash(step), RequestHash: "device-request", ContentHash: "device-response", Source: "datatransfer.command_journal:edge-a", ObservedMS: now - 20, CollectedMS: now - 10, Status: "SUCCESS", Trusted: true, TraceID: "0123456789abcdef0123456789abcdef", Actor: model.Actor{UserID: "engineer", Source: "cloud"}}
	req := model.Execution{DownlinkID: item.ExecutionID, DefinitionID: definition.ID, DefinitionVersion: 1, Status: "completed", Version: 8, Fence: 7, CreatedMS: now - 100, Binding: "immutable-request", Steps: []model.StepResult{{StepID: step.ID, CommandID: item.CommandID, Status: "SUCCESS", StartedMS: now - 50, FinishedMS: item.ObservedMS, EvidenceID: item.ID}}}
	return model.ExecutionReceipt{Execution: req, ReceiptSource: &model.ExecutionReceiptSource{SchemaVersion: 1, NodeID: "edge-a", Version: 8, RecordedMS: now}, CommandEvidence: []model.CommandEvidence{item}}, definition
}

func receiptDelivery(t *testing.T, envelope model.ExecutionReceipt, legacy bool) store.Delivery {
	t.Helper()
	var value any = envelope
	if legacy {
		value = envelope.Execution
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return store.Delivery{ID: fmt.Sprintf("receipt:%s:%d", envelope.DownlinkID, envelope.Version), Kind: "cloud_receipt", Destination: "edge-a", Payload: raw}
}

func checkReceiptEvidenceOrdering(t *testing.T, server *Server) {
	t.Helper()
	ctx := context.Background()
	envelope, definition := receiptExample(t, server.Store)
	// A previously committed pre-extension receipt can already reference an ID.
	if err := server.receive(ctx, "edge-a", receiptDelivery(t, envelope, true)); err != nil {
		t.Fatal(err)
	}
	current, _ := server.Store.Get(ctx, "execution", envelope.DownlinkID)
	older := envelope
	older.Version, older.Fence, older.Status = 6, 2, "ready_to_resume"
	source := *envelope.ReceiptSource
	source.Version = older.Version
	older.ReceiptSource = &source
	if err := server.receive(ctx, "edge-a", receiptDelivery(t, older, false)); err != nil {
		t.Fatal(err)
	}
	if err := server.receive(ctx, "edge-a", receiptDelivery(t, older, false)); err != nil {
		t.Fatal(err)
	}
	after, _ := server.Store.Get(ctx, "execution", envelope.DownlinkID)
	if after.Version != current.Version || store.Hash(after.Data) != store.Hash(current.Data) {
		t.Fatal("older source regressed completed state", after)
	}
	items, err := server.Store.ExecutionEvidence(ctx, envelope.DownlinkID)
	if err != nil || len(items) != 1 || store.Hash(items) != store.Hash(envelope.CommandEvidence) {
		t.Fatal(items, err)
	}
	conflict := older
	conflict.CommandEvidence = append([]model.CommandEvidence{}, older.CommandEvidence...)
	conflict.CommandEvidence[0].Message = "different same-ID content"
	if err := server.receive(ctx, "edge-a", receiptDelivery(t, conflict, false)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("delivery identity conflict accepted", err)
	}
	conflict.Version = 9
	conflict.Status, conflict.Fence = "completed", envelope.Fence
	conflictingSource := *envelope.ReceiptSource
	conflictingSource.Version = 9
	conflict.ReceiptSource = &conflictingSource
	if err := server.receive(ctx, "edge-a", receiptDelivery(t, conflict, false)); !errors.Is(err, store.ErrConflict) {
		t.Fatal("evidence identity conflict accepted", err)
	}
	// New rejected feedback may arrive in an older receipt after a trusted result.
	rejected := envelope.CommandEvidence[0]
	rejected.ID, rejected.Trusted, rejected.Source, rejected.ObservedMS, rejected.Status, rejected.ContentHash = "unavailable-lookup", false, "", 0, "NOT_FOUND", ""
	rejected.Rejection = "command result is not present"
	older.Version = 5
	older.CommandEvidence = []model.CommandEvidence{rejected}
	source.Version = 5
	if err := server.receive(ctx, "edge-a", receiptDelivery(t, older, false)); err != nil {
		t.Fatal(err)
	}
	items, _ = server.Store.ExecutionEvidence(ctx, envelope.DownlinkID)
	if len(items) != 2 {
		t.Fatal(items)
	}
	legacy := envelope
	legacy.Version, legacy.Status = 2, "result_unknown"
	legacy.Steps = []model.StepResult{{StepID: "first", CommandID: envelope.Steps[0].CommandID, Status: "RESULT_UNKNOWN"}}
	if err := server.receive(ctx, "edge-a", receiptDelivery(t, legacy, true)); err != nil {
		t.Fatal(err)
	}
	final, _ := server.Store.Get(ctx, "execution", envelope.DownlinkID)
	if store.Hash(final.Data) != store.Hash(current.Data) {
		t.Fatal("legacy receipt erased confirmed evidence", final)
	}
	// Validate against the historical published command after its current plan changes.
	definition.Version = 2
	definition.Policy.Steps[0].DeviceID = "new-device"
	if _, err := server.Store.Put(ctx, "definition", definition.ID, 1, definition); err != nil {
		t.Fatal(err)
	}
	finalReceipt := envelope
	finalReceipt.Version = 10
	finalSource := *envelope.ReceiptSource
	finalSource.Version = 10
	finalReceipt.ReceiptSource = &finalSource
	if err := server.receive(ctx, "edge-a", receiptDelivery(t, finalReceipt, false)); err != nil {
		t.Fatal("lost historical definition binding", err)
	}
	audits, err := server.Store.AuditList(ctx, envelope.DownlinkID, 100)
	if err != nil {
		t.Fatal(err)
	}
	provenance := false
	for _, audit := range audits {
		if audit.Action == "control.receipt_evidence" {
			raw, _ := json.Marshal(audit.Snapshot)
			var saved struct {
				Source model.ExecutionReceiptSource `json:"receipt_source"`
			}
			_ = json.Unmarshal(raw, &saved)
			provenance = provenance || saved.Source.NodeID == "edge-a" && saved.Source.Version == 6 && saved.Source.RecordedMS == source.RecordedMS
		}
	}
	if !provenance {
		t.Fatal("source version/time missing from signed audit", audits)
	}
	issues, err := server.Store.VerifyAudit(ctx)
	if err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
	t.Log("older source version and fence enrich immutable evidence without changing completed state; duplicated and legacy receipts retain evidence; same-ID altered content rolls back; original published payload and receipt source version/time retained")
}

func TestControlReceiptEvidenceOrderingAndLegacyCompatibility(t *testing.T) {
	f := setup(t)
	checkReceiptEvidenceOrdering(t, f.server)
}

func TestPostgresControlReceiptEvidenceOrderingAndLegacyCompatibility(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "receipt_evidence")
	database, err := store.Open(context.Background(), dsn, "cloud-1", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database.DB.SetMaxOpenConns(3)
	database.DB.SetMaxIdleConns(1)
	defer database.Close()
	checkReceiptEvidenceOrdering(t, &Server{Store: database})
}

func TestControlReceiptRejectsUnboundEvidenceAtomically(t *testing.T) {
	f := setup(t)
	envelope, _ := receiptExample(t, f.cloud)
	mutations := map[string]func(*model.ExecutionReceipt){
		"source_node":           func(r *model.ExecutionReceipt) { r.ReceiptSource.NodeID = "edge-b" },
		"source_version":        func(r *model.ExecutionReceipt) { r.ReceiptSource.Version++ },
		"source_schema":         func(r *model.ExecutionReceipt) { r.ReceiptSource.SchemaVersion++ },
		"source_time":           func(r *model.ExecutionReceipt) { r.ReceiptSource.RecordedMS = 1 },
		"missing_source":        func(r *model.ExecutionReceipt) { r.ReceiptSource = nil },
		"execution":             func(r *model.ExecutionReceipt) { r.CommandEvidence[0].ExecutionID = "another" },
		"command":               func(r *model.ExecutionReceipt) { r.CommandEvidence[0].CommandID = "another:first" },
		"device":                func(r *model.ExecutionReceipt) { r.CommandEvidence[0].DeviceID = "another" },
		"step":                  func(r *model.ExecutionReceipt) { r.CommandEvidence[0].StepID = "another" },
		"payload":               func(r *model.ExecutionReceipt) { r.CommandEvidence[0].PayloadHash = "another" },
		"journal_source":        func(r *model.ExecutionReceipt) { r.CommandEvidence[0].Source = "" },
		"journal_content":       func(r *model.ExecutionReceipt) { r.CommandEvidence[0].ContentHash = "" },
		"journal_time":          func(r *model.ExecutionReceipt) { r.CommandEvidence[0].ObservedMS = 1 },
		"collection_time":       func(r *model.ExecutionReceipt) { r.CommandEvidence[0].CollectedMS += 60000 },
		"unresolved_trusted":    func(r *model.ExecutionReceipt) { r.CommandEvidence[0].Status = "RESULT_UNKNOWN" },
		"conflicting_reference": func(r *model.ExecutionReceipt) { r.Steps[0].EvidenceID = "missing" },
		"contradictory_trusted": func(r *model.ExecutionReceipt) { r.CommandEvidence[0].Status = "FAILURE" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(envelope)
			var candidate model.ExecutionReceipt
			_ = json.Unmarshal(raw, &candidate)
			mutate(&candidate)
			if err := f.server.receive(context.Background(), "edge-a", receiptDelivery(t, candidate, false)); err == nil {
				t.Fatal("accepted unbound evidence")
			}
			items, err := f.cloud.ExecutionEvidence(context.Background(), candidate.DownlinkID)
			if err != nil || len(items) != 0 {
				t.Fatal("rejected receipt persisted evidence", items, err)
			}
			if _, err := f.cloud.Get(context.Background(), "execution", candidate.DownlinkID); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("rejected receipt changed execution", err)
			}
		})
	}
	if err := f.server.receive(context.Background(), "edge-a", receiptDelivery(t, envelope, false)); err != nil {
		t.Fatal("rejected input poisoned delivery identity", err)
	}
}

func TestControlReceiptEvidenceMTLSResponseLossAndCurrentResources(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	envelope, _ := receiptExample(t, f.cloud)
	if err := f.edge.Write(ctx, func(tx *store.Tx) error {
		return tx.Enqueue(receiptDelivery(t, envelope, false).ID, "cloud_receipt", "edge-a", envelope)
	}); err != nil {
		t.Fatal(err)
	}
	f.client.HTTP.Transport = &loseResponse{RoundTripper: f.client.HTTP.Transport}
	if err := f.client.Exchange(ctx); err == nil {
		t.Fatal("response loss was not exercised")
	}
	if err := f.client.Exchange(ctx); err != nil {
		t.Fatal(err)
	}
	remaining, _ := f.edge.Deliveries(ctx, "cloud_receipt", 10)
	if len(remaining) != 0 {
		t.Fatal(remaining)
	}
	service := &control.Service{Store: f.cloud, Identity: f.server.Identity, Definitions: &engine.Service{Store: f.cloud}, NodeID: "cloud-1"}
	principal := identity.Principal{User: model.User{ID: "engineer"}}
	detail, err := service.Detail(ctx, principal, envelope.DownlinkID)
	if err != nil || store.Hash(detail.Evidence) != store.Hash(envelope.CommandEvidence) {
		t.Fatal(detail, err)
	}
	feedback := 0
	for _, entry := range detail.Timeline {
		if entry.Kind == "feedback" {
			feedback++
			if entry.TraceID != envelope.CommandEvidence[0].TraceID {
				t.Fatal(entry)
			}
		}
	}
	if feedback != 1 {
		t.Fatal("duplicated or missing feedback timeline", feedback)
	}
	if _, err := f.cloud.Put(ctx, "control_operation", "read-result", 0, model.ControlOperation{ID: "read-result", ExecutionID: envelope.DownlinkID, Result: &envelope.Execution, Version: 1}); err != nil {
		t.Fatal(err)
	}
	doc, err := f.cloud.Get(ctx, "entity", "counter")
	if err != nil {
		t.Fatal(err)
	}
	device, _ := store.Decode[model.Entity](doc)
	device.ParentID, device.Version = "restricted-factory", doc.Version+1
	if _, err := f.cloud.Put(ctx, "entity", device.ID, doc.Version, device); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Detail(ctx, principal, envelope.DownlinkID); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("current resource ancestry was bypassed by imported evidence", err)
	}
	if _, err := service.Operation(ctx, principal, "read-result"); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("operation result exposed revoked original resource", err)
	}
	t.Log("actual authenticated exchange committed evidence before lost response, replay acknowledged once, feedback retained original trace; current moved-device access denied for detail and operation result")
}

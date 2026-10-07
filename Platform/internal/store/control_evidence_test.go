package store

import (
	"context"
	"errors"
	"testing"

	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func checkControlEvidenceAtomicity(t *testing.T, database *Store) {
	t.Helper()
	ctx := context.Background()
	now := database.Now().UnixMilli()
	item := model.CommandEvidence{ID: "original-feedback", ExecutionID: "生产线:证据", CommandID: "生产线:证据:first", DeviceID: "device", StepID: "first", Source: "command_journal:edge-a", ObservedMS: now, CollectedMS: now, PayloadHash: "payload", ContentHash: "content", Status: "SUCCESS", Trusted: true}
	req := model.Execution{DownlinkID: item.ExecutionID, DefinitionID: "plan", Version: 1, Status: "ready_to_resume", Steps: []model.StepResult{{CommandID: item.CommandID, StepID: item.StepID, Status: "SUCCESS", EvidenceID: item.ID}}}
	transition := model.ExecutionTransition{ExecutionID: req.DownlinkID, Version: 1, Event: "reconcile", To: req.Status, AtMS: now, EvidenceIDs: []string{item.ID}}
	write := func(tx *Tx) error {
		if err := tx.SaveControlEvidence(item); err != nil {
			return err
		}
		return tx.PersistExecution(req, 0, transition, "edge-a")
	}
	rollback := errors.New("injected failure after evidence state audit and outbox")
	if err := database.Write(ctx, func(tx *Tx) error {
		if err := write(tx); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	for _, table := range []string{"control_evidence", "execution_transitions", "outbox", "audit"} {
		var count int
		if err := database.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial rolled back control transaction", table, count, err)
		}
	}
	if err := database.Write(ctx, write); err != nil {
		t.Fatal(err)
	}
	if err := database.Write(ctx, func(tx *Tx) error { return tx.SaveControlEvidence(item) }); err != nil {
		t.Fatal(err)
	}
	changed := item
	changed.ContentHash = "conflicting-content"
	if err := database.Write(ctx, func(tx *Tx) error { return tx.SaveControlEvidence(changed) }); !errors.Is(err, ErrConflict) {
		t.Fatal("same evidence identity changed", err)
	}
	saved, err := database.ExecutionEvidence(ctx, req.DownlinkID)
	if err != nil || len(saved) != 1 || Hash(saved[0]) != Hash(item) {
		t.Fatal(saved, err)
	}
	deliveries, err := database.Deliveries(ctx, "cloud_receipt", 10)
	if err != nil || len(deliveries) != 1 {
		t.Fatal(deliveries, err)
	}
	var receipt model.ExecutionReceipt
	if err := DecodeJSON(deliveries[0].Payload, &receipt); err != nil || receipt.ReceiptSource == nil || receipt.ReceiptSource.NodeID != "edge-a" || receipt.ReceiptSource.Version != 1 || receipt.ReceiptSource.RecordedMS != now || Hash(receipt.CommandEvidence) != Hash(saved) {
		t.Fatal(receipt, err)
	}
	var legacy model.Execution
	if err := DecodeJSON(deliveries[0].Payload, &legacy); err != nil || Hash(legacy) != Hash(req) {
		t.Fatal("execution root compatibility changed", legacy, err)
	}
	issues, err := database.VerifyAudit(ctx)
	if err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
	t.Log("evidence, local revision, transition, receipt and signed audit roll back together; immutable evidence replay retained once; additive receipt remains readable as Execution")
}

func TestControlEvidenceAtomicReceiptAndImmutableIdentity(t *testing.T) {
	checkControlEvidenceAtomicity(t, testStore(t))
}

func TestPostgresControlEvidenceAtomicReceiptAndImmutableIdentity(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "control_evidence")
	database, err := Open(context.Background(), dsn, "edge-a", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database.DB.SetMaxOpenConns(3)
	database.DB.SetMaxIdleConns(1)
	defer database.Close()
	checkControlEvidenceAtomicity(t, database)
}

package coordination

import (
	"competition2026/product/platform/internal/control"
	"errors"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestRealThreeReplicaReadOnlyReconcileAndManualResume(t *testing.T) {
	f := realSite(t)
	ctx := f.ctx
	u := model.User{ID: "engineer", Name: "Engineer", Roles: []string{"engineer"}, Resources: []string{"*"}, Active: true, Version: 1}
	for _, s := range f.stores {
		if _, err := s.Put(ctx, "user", u.ID, 0, u); err != nil {
			t.Fatal(err)
		}
	}
	p := identity.Principal{User: u, Actor: model.Actor{UserID: u.ID}}
	f.devices[1].loseResponse = true
	req, err := f.services[0].Run(ctx, execution(f, "人工:现场:恢复"), true)
	if err != nil || req.Status != "result_unknown" {
		t.Fatal(req, err)
	}
	previous := req
	if _, err = f.services[0].Run(ctx, req, true); err != nil {
		t.Fatal(err)
	}
	first, second, third := f.definition.Policy.Steps[0], f.definition.Policy.Steps[1], f.definition.Policy.Steps[2]
	if f.devices[0].count(req.DownlinkID+":"+first.ID) != 1 || f.devices[1].count(req.DownlinkID+":"+second.ID) != 1 || f.devices[2].count(req.DownlinkID+":"+third.ID) != 0 {
		t.Fatal("unexpected pre-reconcile device counts")
	}
	req, err = f.services[0].Reconcile(ctx, p, req.DownlinkID, model.ExecutionAction{ExpectedVersion: req.Version, Reason: "现场命令日志已确认"})
	if err != nil || req.Status != "ready_to_resume" || req.Fence <= previous.Fence {
		t.Fatal(req, err)
	}
	if err = f.nodes[0].Validate(ctx, previous.DownlinkID, previous.CoordinatorID, previous.Fence); err == nil {
		t.Fatal("old coordinator fence accepted")
	}
	if err = f.nodes[0].Checkpoint(ctx, previous); err == nil {
		t.Fatal("old coordinator journal overwrite accepted")
	}
	evidence, err := f.stores[0].ExecutionEvidence(ctx, req.DownlinkID)
	if err != nil || len(evidence) != 1 || !evidence[0].Trusted || evidence[0].Source != "simulated-device:edge-b" {
		t.Fatal(evidence, err)
	}
	if f.devices[1].count(req.DownlinkID+":"+second.ID) != 1 {
		t.Fatal("read-only result query sent a command")
	}
	if _, err = f.services[0].Resume(ctx, p, req.DownlinkID, model.ExecutionAction{ExpectedVersion: previous.Version, Reason: "stale action"}); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	req, err = f.services[0].Resume(ctx, p, req.DownlinkID, model.ExecutionAction{ExpectedVersion: req.Version, Reason: "继续剩余设备步骤"})
	if err != nil {
		t.Fatal(err)
	}
	req, err = f.services[0].Run(ctx, req, true)
	if err != nil || req.Status != "completed" {
		t.Fatal(req, err)
	}
	for i, step := range f.definition.Policy.Steps {
		if count := f.devices[i].count(req.DownlinkID + ":" + step.ID); count != 1 {
			t.Fatal(step.ID, count)
		}
	}
	journal, err := f.nodes[2].Recover(ctx, req.DownlinkID)
	if err != nil || journal.Status != "completed" || len(journal.Steps) != 3 {
		t.Fatal(journal, err)
	}
	for _, node := range f.nodes {
		info, err := node.JS.StreamInfo("KV_" + node.Prefix + "_journal")
		if err != nil || info.Config.Replicas != 3 {
			t.Fatal(info, err)
		}
	}
	t.Logf("actual signed NATS result query; initial fence=%d final fence=%d; recovered command=%s source=%s; all three physical command counts=1; old fence rejected", previous.Fence, req.Fence, evidence[0].CommandID, evidence[0].Source)
}

func TestRealConcurrentOperationDeliveryKeepsLeaseContentionRetryable(t *testing.T) {
	f := realSite(t)
	ctx := f.ctx
	u := model.User{ID: "operator", Active: true, Roles: []string{"engineer"}, Resources: []string{"*"}, Version: 1}
	if _, err := f.stores[0].Put(ctx, "user", u.ID, 0, u); err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{User: u, Actor: model.Actor{UserID: u.ID}}
	f.devices[1].loseResponse = true
	req, err := f.services[0].Run(ctx, execution(f, "concurrent-operation"), true)
	if err != nil || req.Status != "result_unknown" {
		t.Fatal(req, err)
	}
	f.devices[1].lookupEntered = make(chan string, 1)
	f.devices[1].lookupRelease = make(chan struct{})
	result := make(chan model.ControlOperation, 1)
	resultErr := make(chan error, 1)
	go func() {
		op, err := f.services[0].SubmitOperation(ctx, p, req.DownlinkID, "reconcile", model.ExecutionAction{ExpectedVersion: req.Version, Reason: "同请求同步处理与工作线程竞争", OperationID: "concurrent-reconcile"})
		result <- op
		resultErr <- err
	}()
	select {
	case <-f.devices[1].lookupEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("query did not start")
	}
	delivery, err := f.stores[0].Delivery(ctx, "control-op:concurrent-reconcile")
	if err != nil {
		t.Fatal(err)
	}
	var envelope model.ControlOperationEnvelope
	if err = store.DecodeJSON(delivery.Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if err = f.services[0].ProcessOperation(ctx, envelope); !errors.Is(err, control.ErrLeaseHeld) {
		t.Fatal("duplicate lease result", err)
	}
	doc, err := f.stores[0].Get(ctx, "control_operation", envelope.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := store.Decode[model.ControlOperation](doc)
	if pending.Status != "pending" {
		t.Fatal(pending)
	}
	close(f.devices[1].lookupRelease)
	op := <-result
	if err = <-resultErr; err != nil || op.Status != "completed" || op.Result.Status != "ready_to_resume" {
		t.Fatal(op, err)
	}
	if err = f.services[0].ProcessOperation(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	if f.devices[1].count(req.DownlinkID+":"+f.definition.Policy.Steps[1].ID) != 1 {
		t.Fatal("duplicate physical command")
	}
	t.Log("synchronous handler and durable worker overlapped inside signed read-only query; duplicate ErrLeaseHeld kept pending; first completed; duplicate retry returned existing result; physical_actions=1")
}

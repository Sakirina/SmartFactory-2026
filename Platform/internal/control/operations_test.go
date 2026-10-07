package control

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type firstLookupBlocked struct {
	*evidenceDispatcher
	entered chan struct{}
	release chan struct{}
	queries atomic.Int64
}

func (d *firstLookupBlocked) LookupCommand(ctx context.Context, step model.Step, id string) (CommandFeedback, error) {
	if d.queries.Add(1) == 1 {
		close(d.entered)
		select {
		case <-ctx.Done():
			return CommandFeedback{}, ctx.Err()
		case <-d.release:
		}
	}
	return d.evidenceDispatcher.LookupCommand(ctx, step, id)
}

func TestOperationStartSurvivesDuplicateAfterDeadline(t *testing.T) {
	ctx := context.Background()
	edge, cloud, users, device, edgeReq, cloudReq := operationPair(t, "blocked-deadline")
	now := edge.Store.CurrentTime()
	var clock atomic.Int64
	clock.Store(now.UnixMilli())
	edge.Identity.Store.Now = func() time.Time { return time.UnixMilli(clock.Load()) }
	deadline := now.Add(time.Second).UnixMilli()
	source := edgeReq.Version
	op, err := cloud.SubmitOperation(ctx, users["engineer"], cloudReq.DownlinkID, "reconcile", model.ExecutionAction{ExpectedVersion: cloudReq.Version, ExpectedSourceVersion: &source, DeadlineMS: deadline, OperationID: "blocked-deadline-operation", Reason: "并发核对保留首次处理时间"})
	if err != nil {
		t.Fatal(err)
	}
	d := &firstLookupBlocked{evidenceDispatcher: device, entered: make(chan struct{}), release: make(chan struct{})}
	edge.Dispatcher = d
	envelope := model.ControlOperationEnvelope{Operation: op, Execution: cloudReq}
	first := make(chan error, 1)
	go func() { first <- edge.ProcessOperation(ctx, envelope) }()
	select {
	case <-d.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first read-only query did not start")
	}
	started, err := edge.operation(ctx, op.ID)
	if err != nil || started.StartedMS != now.UnixMilli() || started.Status != "pending" {
		t.Fatal(started, err)
	}
	if err = cloud.Store.Write(ctx, func(tx *store.Tx) error { return ReceiveOperationReceipt(tx, "edge-a", started) }); err != nil {
		t.Fatal(err)
	}
	cloudStarted, err := cloud.operation(ctx, op.ID)
	if err != nil || cloudStarted.Status != "pending" || cloudStarted.StartedMS != started.StartedMS {
		t.Fatal(cloudStarted, err)
	}
	clock.Store(now.Add(2 * time.Second).UnixMilli())
	duplicateErr := edge.ProcessOperation(ctx, envelope)
	close(d.release)
	if err = <-first; err != nil || duplicateErr != nil {
		t.Fatal(err, duplicateErr)
	}
	final, err := edge.operation(ctx, op.ID)
	if err != nil || final.Status != "completed" || final.StartedMS != started.StartedMS || final.ProcessedMS <= deadline || final.Result.Status != "ready_to_resume" {
		t.Fatal(final, err)
	}
	if err = cloud.Store.Write(ctx, func(tx *store.Tx) error { return ReceiveOperationReceipt(tx, "edge-a", final) }); err != nil {
		t.Fatal(err)
	}
	if err = cloud.Store.Write(ctx, func(tx *store.Tx) error { return ReceiveOperationReceipt(tx, "edge-a", started) }); err != nil {
		t.Fatal("late started receipt", err)
	}
	if len(device.counts()) != 1 || device.counts()[edgeReq.DownlinkID+":step-a"] != 1 {
		t.Fatal(device.counts())
	}
	t.Logf("first query blocked before deadline, duplicate after deadline: started=%d deadline=%d completed=%d edge/cloud completed; physical_actions=%v", final.StartedMS, deadline, final.ProcessedMS, device.counts())
}

func TestStartedOperationResumesAfterDatabaseReopenBeyondDeadline(t *testing.T) {
	ctx := context.Background()
	edge, cloud, users, device, edgeReq, cloudReq := operationPair(t, "started-restart")
	now := edge.Store.CurrentTime()
	deadline := now.Add(time.Second).UnixMilli()
	source := edgeReq.Version
	op, err := cloud.SubmitOperation(ctx, users["engineer"], cloudReq.DownlinkID, "reconcile", model.ExecutionAction{ExpectedVersion: cloudReq.Version, ExpectedSourceVersion: &source, DeadlineMS: deadline, OperationID: "started-restart-operation", Reason: "进程恢复继续已开始的核对"})
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("process exited after durable processing start")
	edge.Fault = func(_ context.Context, phase string, _ model.Execution, _ string) error {
		if phase == "after_operation_started" {
			return crash
		}
		return nil
	}
	envelope := model.ControlOperationEnvelope{Operation: op, Execution: cloudReq}
	if err = edge.ProcessOperation(ctx, envelope); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	started, err := edge.operation(ctx, op.ID)
	if err != nil || started.StartedMS == 0 {
		t.Fatal(started, err)
	}
	var seq int
	var name, path string
	if err = edge.Identity.Store.DB.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err = edge.Identity.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, path, edge.NodeID, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	reopened.Now = func() time.Time { return now.Add(2 * time.Second) }
	edge.Store, edge.Identity.Store, edge.Definitions.Store = reopened, reopened, reopened
	edge.Fault = nil
	if err = edge.ProcessOperation(ctx, envelope); err != nil {
		t.Fatal(err)
	}
	final, err := edge.operation(ctx, op.ID)
	if err != nil || final.Status != "completed" || final.StartedMS != started.StartedMS || final.ProcessedMS <= deadline {
		t.Fatal(final, err)
	}
	if len(device.counts()) != 1 {
		t.Fatal(device.counts())
	}
	t.Logf("SQLite reopened after durable start; original_start=%d deadline=%d completed=%d physical_actions=%v", final.StartedMS, deadline, final.ProcessedMS, device.counts())
}

func operationPair(t *testing.T, id string) (*Service, *Service, map[string]identity.Principal, *evidenceDispatcher, model.Execution, model.Execution) {
	t.Helper()
	ctx := context.Background()
	edge, users, _ := fixture(t)
	twoSteps(t, edge)
	device := deviceEvidence(edge)
	device.uncertain = true
	edgeReq := queued(t, edge, users, id)
	edgeReq, err := edge.Run(ctx, edgeReq, false)
	if err != nil {
		t.Fatal(err)
	}
	cloud, cloudUsers, _ := fixture(t)
	twoSteps(t, cloud)
	cloudReq := queued(t, cloud, cloudUsers, id)
	next := edgeReq
	if err = cloud.Store.Write(ctx, func(tx *store.Tx) error {
		if err := ApplyTransition(tx, &next, cloudReq.Version, "receipt", TransitionOptions{Source: "edge-a", SourceVersion: edgeReq.Version}); err != nil {
			return err
		}
		return tx.SetEphemeral("receipt_cursor", "edge-a:"+id, edgeReq.Version)
	}); err != nil {
		t.Fatal(err)
	}
	cloud.Edge = false
	cloud.NodeID = "cloud"
	return edge, cloud, cloudUsers, device, edgeReq, next
}
func TestOperationDeadlineUsesStartTimeAndCloudAcceptsLateCompletion(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "starts_before_deadline_finishes_after", true: "expired_before_start"}[expired], func(t *testing.T) {
			edge, cloud, users, device, edgeReq, cloudReq := operationPair(t, "deadline-case")
			now := edge.Store.CurrentTime()
			deadline := now.Add(time.Second).UnixMilli()
			source := edgeReq.Version
			op, err := cloud.SubmitOperation(context.Background(), users["engineer"], cloudReq.DownlinkID, "reconcile", model.ExecutionAction{ExpectedVersion: cloudReq.Version, ExpectedSourceVersion: &source, DeadlineMS: deadline, OperationID: "deadline-op", Reason: "核对原始命令结果"})
			if err != nil || op.Status != "pending" {
				t.Fatal(op, err)
			}
			if expired {
				edge.Identity.Store.Now = func() time.Time { return now.Add(2 * time.Second) }
			} else {
				device.alter = func(_ *CommandFeedback) {
					edge.Identity.Store.Now = func() time.Time { return now.Add(2 * time.Second) }
				}
			}
			envelope := model.ControlOperationEnvelope{Operation: op, Execution: cloudReq}
			if err = edge.ProcessOperation(context.Background(), envelope); err != nil {
				t.Fatal(err)
			}
			result, err := edge.operation(context.Background(), op.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := "completed"
			state := "ready_to_resume"
			if expired {
				expected = "expired"
				state = "result_unknown"
			}
			if result.Status != expected {
				t.Fatal(result)
			}
			if !expired && (result.StartedMS >= deadline || result.ProcessedMS <= deadline) {
				t.Fatal(result)
			}
			local, err := edge.Get(context.Background(), edgeReq.DownlinkID)
			if err != nil || local.Status != state {
				t.Fatal(local, err)
			}
			if err = cloud.Store.Write(context.Background(), func(tx *store.Tx) error { return ReceiveOperationReceipt(tx, "edge-a", result) }); err != nil {
				t.Fatal(err)
			}
			final, err := cloud.operation(context.Background(), op.ID)
			if err != nil || final.Status != result.Status {
				t.Fatal(final, err)
			}
			// Duplicate transport delivery and duplicate receipts preserve both versions.
			if err = edge.ProcessOperation(context.Background(), envelope); err != nil {
				t.Fatal(err)
			}
			if err = cloud.Store.Write(context.Background(), func(tx *store.Tx) error { return ReceiveOperationReceipt(tx, "edge-a", result) }); err != nil {
				t.Fatal(err)
			}
			again, _ := cloud.operation(context.Background(), op.ID)
			if again.Version != final.Version {
				t.Fatal(again.Version, final.Version)
			}
			t.Logf("edge/cloud=%s/%s deadline=%d started=%d completed=%d execution=%s sends=%v", result.Status, final.Status, deadline, result.StartedMS, result.ProcessedMS, local.Status, device.counts())
		})
	}
}
func TestCloudPendingConflictAndCurrentPermissionAreCheckedAtEdge(t *testing.T) {
	for _, scenario := range []string{"revision_changed", "permission_revoked"} {
		t.Run(scenario, func(t *testing.T) {
			edge, cloud, users, device, edgeReq, cloudReq := operationPair(t, "offline-op")
			source := edgeReq.Version
			input := model.ExecutionAction{ExpectedVersion: cloudReq.Version, ExpectedSourceVersion: &source, OperationID: "offline-op-request", Reason: "云端等待现场核对"}
			op, err := cloud.SubmitOperation(context.Background(), users["engineer"], cloudReq.DownlinkID, "reconcile", input)
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err := cloud.SubmitOperation(context.Background(), users["engineer"], cloudReq.DownlinkID, "reconcile", input)
			if err != nil || duplicate.ID != op.ID || duplicate.Version != op.Version {
				t.Fatal(duplicate, err)
			}
			input.Reason = "different content"
			if _, err = cloud.SubmitOperation(context.Background(), users["engineer"], cloudReq.DownlinkID, "reconcile", input); !errors.Is(err, store.ErrConflict) {
				t.Fatal(err)
			}
			unchanged, _ := cloud.Get(context.Background(), cloudReq.DownlinkID)
			if unchanged.Status != "result_unknown" || unchanged.Version != cloudReq.Version {
				t.Fatal(unchanged)
			}
			if scenario == "revision_changed" {
				if _, err = edge.Cancel(context.Background(), users["engineer"], edgeReq.DownlinkID, model.ExecutionAction{ExpectedVersion: edgeReq.Version, Reason: "现场先行取消"}); err != nil {
					t.Fatal(err)
				}
			} else {
				doc, _ := edge.Store.Get(context.Background(), "user", "engineer")
				u, _ := store.Decode[model.User](doc)
				u.Active = false
				if _, err = edge.Identity.Store.Put(context.Background(), "user", u.ID, doc.Version, u); err != nil {
					t.Fatal(err)
				}
			}
			if err = edge.ProcessOperation(context.Background(), model.ControlOperationEnvelope{Operation: op, Execution: cloudReq}); err != nil {
				t.Fatal(err)
			}
			result, _ := edge.operation(context.Background(), op.ID)
			if result.Status != "rejected" || result.Error == "" {
				t.Fatal(result)
			}
			if err = cloud.Store.Write(context.Background(), func(tx *store.Tx) error { return ReceiveOperationReceipt(tx, "edge-a", result) }); err != nil {
				t.Fatal(err)
			}
			if len(device.counts()) != 1 {
				t.Fatal(device.counts())
			}
		})
	}
}

func TestLocalOperationSurvivesCommitBeforeProcessingAndSameIDRetry(t *testing.T) {
	for _, viaWorker := range []bool{true, false} {
		t.Run(map[bool]string{true: "worker_delivery", false: "same_id_retry"}[viaWorker], func(t *testing.T) {
			ctx := context.Background()
			s, users, _ := fixture(t)
			d := deviceEvidence(s)
			req := queued(t, s, users, "pending-on-restart")
			input := model.ExecutionAction{ExpectedVersion: req.Version, Reason: "提交后进程中断", OperationID: "durable-local-operation"}
			crash := errors.New("injected shutdown after operation commit")
			s.Fault = func(_ context.Context, phase string, _ model.Execution, _ string) error {
				if phase == "after_operation_persist" {
					return crash
				}
				return nil
			}
			if _, err := s.SubmitOperation(ctx, users["engineer"], req.DownlinkID, "cancel", input); !errors.Is(err, crash) {
				t.Fatal(err)
			}
			op, err := s.operation(ctx, input.OperationID)
			if err != nil || op.Status != "pending" {
				t.Fatal(op, err)
			}
			deliveries, err := s.Identity.Store.Deliveries(ctx, "edge_control_operation", 100)
			if err != nil || len(deliveries) != 1 {
				t.Fatal(deliveries, err)
			}
			db := s.Identity.Store
			var seq int
			var name, path string
			if err = db.DB.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			now := db.Now
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(ctx, path, s.NodeID, make([]byte, 32))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reopened.Close() })
			reopened.Now = now
			s.Store = reopened
			s.Identity.Store = reopened
			s.Definitions.Store = reopened
			s.Fault = nil
			if viaWorker {
				var envelope model.ControlOperationEnvelope
				if err = store.DecodeJSON(deliveries[0].Payload, &envelope); err != nil {
					t.Fatal(err)
				}
				if err = s.ProcessOperation(ctx, envelope); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = s.SubmitOperation(ctx, users["engineer"], req.DownlinkID, "cancel", input); err != nil {
					t.Fatal(err)
				}
			}
			final, err := s.operation(ctx, op.ID)
			if err != nil || final.Status != "completed" || final.Result == nil || final.Result.Status != "cancelled" {
				t.Fatal(final, err)
			}
			duplicate, err := s.SubmitOperation(ctx, users["engineer"], req.DownlinkID, "cancel", input)
			if err != nil || duplicate.Version != final.Version {
				t.Fatal(duplicate, err)
			}
			if len(d.counts()) != 0 {
				t.Fatal("cancel caused device action", d.counts())
			}
			assertAtomicHistory(t, s, *final.Result)
			t.Logf("pending committed with durable delivery, actual SQLite reopen, worker=%t, final=%s/%s physical_actions=0", viaWorker, final.Status, final.Result.Status)
		})
	}
}

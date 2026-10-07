package application_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/businessfixture"
	"competition2026/product/platform/internal/store"
)

func checkBusinessConcurrentHandover(t *testing.T, f *businessfixture.Fixture) {
	ctx := context.Background()
	w, e := f.Business.CreateWorkOrder(ctx, f.Principal, application.CreateWorkOrderInput{ID: "concurrent-handover", RequestID: "concurrent-handover", Title: "并发交班验证", GroupID: "factory", AssigneeID: f.Principal.User.ID, AlarmIDs: []string{f.AlarmID}, Reason: "检查版本保护"})
	if e != nil {
		t.Fatal(e)
	}
	input := application.HandoverInput{ExpectedVersion: w.WorkOrder.Version, FromUserID: f.Principal.User.ID, ToUserID: f.OtherPrincipal.User.ID, PendingItems: []string{"检查后续观测"}, Reason: "同一工单并发交班"}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			in := input
			in.RequestID = fmt.Sprintf("handover-attempt-%d", i)
			_, e := f.Business.HandoverWorkOrder(ctx, f.Principal, w.WorkOrder.ID, in)
			results <- e
		}(i)
	}
	close(start)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			success++
		} else if errors.Is(e, store.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	current, e := f.Business.WorkOrderDetail(ctx, f.OtherPrincipal, w.WorkOrder.ID)
	if e != nil || success != 1 || conflict != 1 || len(current.Handovers) != 1 || current.WorkOrder.Version != 2 || len(current.WorkOrder.Entries) != 2 {
		t.Fatal(current, success, conflict, e)
	}
	failed := errors.New("handover-post-mutation-injected-failure")
	f.Business.Store = &businessWriteHook{Store: f.Store, fail: failed, failKind: "work_order", failID: w.WorkOrder.ID, failVersion: 3}
	_, e = f.Business.HandoverWorkOrder(ctx, f.OtherPrincipal, w.WorkOrder.ID, application.HandoverInput{RequestID: "rollback-handover", ExpectedVersion: current.WorkOrder.Version, FromUserID: f.OtherPrincipal.User.ID, ToUserID: f.Principal.User.ID, PendingItems: []string{"验证事务回滚"}, Reason: "审计和交班写入后注入失败"})
	if !errors.Is(e, failed) {
		t.Fatal(e)
	}
	f.Business.Store = f.Store
	after, e := f.Business.WorkOrderDetail(ctx, f.OtherPrincipal, w.WorkOrder.ID)
	if e != nil || after.WorkOrder.Version != current.WorkOrder.Version || len(after.Handovers) != 1 || after.WorkOrder.AssigneeID != f.OtherPrincipal.User.ID {
		t.Fatal(after, e)
	}
	if issues, e := f.Store.VerifyAudit(ctx); e != nil || len(issues) > 0 {
		t.Fatal(issues, e)
	}
	t.Log("concurrent handover commits one responsibility transfer; failure injected after new work-order revision rolls back responsibility, handover, operation receipt and audit together")
}

func TestBusinessConcurrentHandoverSQLite(t *testing.T) {
	checkBusinessConcurrentHandover(t, businessFixture(t, false))
}
func TestBusinessConcurrentHandoverPostgres(t *testing.T) {
	checkBusinessConcurrentHandover(t, businessFixture(t, true))
}

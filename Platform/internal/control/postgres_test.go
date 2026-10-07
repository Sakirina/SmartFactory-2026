package control

import (
	"context"
	"errors"
	"testing"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func TestPostgresControlAtomicTransitionsAndConcurrentIntervention(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "control")
	ctx := context.Background()
	database, err := store.Open(ctx, dsn, "edge-a", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database.DB.SetMaxOpenConns(3)
	database.DB.SetMaxIdleConns(1)
	defer database.Close()
	s, users, _ := controlFixture(t, database)
	twoSteps(t, s)
	device := deviceEvidence(s)
	device.uncertain = true
	req := queued(t, s, users, "生产线:PG:核对")
	before, err := s.Store.ExecutionTransitions(ctx, req.DownlinkID)
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback after full transition write")
	next := req
	next.Status = "cancelled"
	err = database.Write(ctx, func(tx *store.Tx) error {
		if e := ApplyTransition(tx, &next, req.Version, "cancel", s.transitionOptions(next)); e != nil {
			return e
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	current, err := s.Get(ctx, req.DownlinkID)
	if err != nil || current.Version != req.Version || current.Status != req.Status {
		t.Fatal(current, err)
	}
	after, _ := s.Store.ExecutionTransitions(ctx, req.DownlinkID)
	if len(before) != len(after) {
		t.Fatal("rolled-back transition survived")
	}
	req, err = s.Run(ctx, req, false)
	if err != nil || req.Status != "result_unknown" {
		t.Fatal(req, err)
	}
	op, err := s.SubmitOperation(ctx, users["engineer"], req.DownlinkID, "reconcile", model.ExecutionAction{ExpectedVersion: req.Version, Reason: "查询原始设备命令日志", OperationID: "pg-reconcile"})
	if err != nil || op.Status != "completed" || op.Result.Status != "ready_to_resume" {
		t.Fatal(op, err)
	}
	req, err = s.Resume(ctx, users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: op.ResultVersion, Reason: "完成剩余动作"})
	if err != nil {
		t.Fatal(err)
	}
	device.uncertain = false
	req, err = s.Run(ctx, req, false)
	if err != nil || req.Status != "completed" {
		t.Fatal(req, err)
	}
	assertAtomicHistory(t, s, req)
	second, err := store.Open(ctx, dsn, "edge-a", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	second.DB.SetMaxOpenConns(3)
	second.DB.SetMaxIdleConns(1)
	defer second.Close()
	second.Now = database.Now
	s2 := &Service{Store: second, Identity: &identity.Manager{Store: second, Master: make([]byte, 32)}, Definitions: &engine.Service{Store: second}, NodeID: "edge-a", Edge: true}
	contended := queued(t, s, users, "pg-cas")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, service := range []*Service{s, s2} {
		go func() {
			<-start
			_, err := service.Cancel(ctx, users["engineer"], contended.DownlinkID, model.ExecutionAction{ExpectedVersion: contended.Version, Reason: "同版本并发操作"})
			results <- err
		}()
	}
	close(start)
	wins, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			wins++
		} else if errors.Is(e, store.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
	final, err := s.Get(ctx, contended.DownlinkID)
	if err != nil {
		t.Fatal(err)
	}
	assertAtomicHistory(t, s, final)
	t.Logf("PostgreSQL isolated schema; two independent application pools(3+3), manager1; full transition rollback; Unicode action lookup/reconcile/resume=%s counts=%v; concurrent CAS winners=%d conflicts=%d", req.Status, device.counts(), wins, conflicts)
}

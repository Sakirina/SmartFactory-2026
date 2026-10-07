package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type evidenceDispatcher struct {
	mu        sync.Mutex
	sends     map[string]int
	records   map[string]CommandFeedback
	now       func() time.Time
	uncertain bool
	alter     func(*CommandFeedback)
	entered   chan string
	release   chan struct{}
}

func (d *evidenceDispatcher) Send(ctx context.Context, step model.Step, id string, _ int64) (DispatchResult, error) {
	d.mu.Lock()
	d.sends[id]++
	d.records[id] = CommandFeedback{CommandID: id, DeviceID: step.DeviceID, PayloadHash: store.Hash(step), RequestHash: store.Hash(step), Source: "simulated-device:durable-command-journal", ObservedMS: d.now().UnixMilli(), Found: true, Bound: true, Status: "SUCCESS", ContentHash: store.Hash([]any{id, step, "SUCCESS"})}
	uncertain := d.uncertain
	d.mu.Unlock()
	if d.entered != nil {
		d.entered <- id
		select {
		case <-ctx.Done():
			return DispatchResult{}, ctx.Err()
		case <-d.release:
		}
	}
	if uncertain {
		return DispatchResult{}, errors.New("feedback connection lost after device action")
	}
	return DispatchResult{Status: "SUCCESS"}, nil
}
func (d *evidenceDispatcher) LookupCommand(_ context.Context, _ model.Step, id string) (CommandFeedback, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := d.records[id]
	if d.alter != nil {
		d.alter(&result)
	}
	return result, nil
}
func (d *evidenceDispatcher) counts() map[string]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]int{}
	for k, v := range d.sends {
		out[k] = v
	}
	return out
}
func twoSteps(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	doc, err := s.Store.Get(ctx, "definition", "plan")
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.Decode[model.Definition](doc)
	if err != nil {
		t.Fatal(err)
	}
	d.Version++
	d.Policy.Steps = append(d.Policy.Steps, model.Step{ID: "step-b", DeviceID: "device", EdgeID: "edge-a", Action: "actuate", TimeoutMS: 10000})
	d.Policy.Steps[0].TimeoutMS = 10000
	if _, err = s.Identity.Store.Put(ctx, "definition", d.ID, doc.Version, d); err != nil {
		t.Fatal(err)
	}
}
func queued(t *testing.T, s *Service, users map[string]identity.Principal, id string) model.Execution {
	t.Helper()
	req := approvals(t, s, users, id, false)
	req, err := s.Dispatch(context.Background(), users["engineer"], req.DownlinkID)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
func deviceEvidence(s *Service) *evidenceDispatcher {
	d := &evidenceDispatcher{sends: map[string]int{}, records: map[string]CommandFeedback{}, now: s.Store.CurrentTime}
	s.Dispatcher = d
	return d
}
func assertAtomicHistory(t *testing.T, s *Service, req model.Execution) {
	t.Helper()
	ctx := context.Background()
	transitions, err := s.Store.ExecutionTransitions(ctx, req.DownlinkID)
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != int(req.Version) {
		t.Fatalf("history=%d version=%d", len(transitions), req.Version)
	}
	audit, err := s.Store.AuditList(ctx, req.DownlinkID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for i, event := range transitions {
		if event.Version != int64(i+1) || event.PreviousVersion != int64(i) {
			t.Fatal(event)
		}
		if _, err := s.Identity.Store.Delivery(ctx, fmt.Sprintf("receipt:%s:%d", req.DownlinkID, event.Version)); err != nil {
			t.Fatal("missing atomic receipt", err)
		}
	}
	if len(audit) < len(transitions) {
		t.Fatal("missing atomic audit", len(audit), len(transitions))
	}
}
func TestCrashWindowsRecoverWithoutRepeatingReservedCommands(t *testing.T) {
	for _, phase := range []string{"before_reserve", "after_reserve", "after_send", "before_result_save", "after_result_save"} {
		t.Run(phase, func(t *testing.T) {
			s, users, _ := fixture(t)
			d := deviceEvidence(s)
			req := queued(t, s, users, "crash:"+phase)
			crash := errors.New("injected process exit")
			s.Fault = func(_ context.Context, p string, _ model.Execution, _ string) error {
				if p == phase {
					return crash
				}
				return nil
			}
			if _, err := s.Run(context.Background(), req, false); !errors.Is(err, crash) {
				t.Fatal(err)
			}
			oldStore := s.Identity.Store
			var seq int
			var name, path string
			if err := oldStore.DB.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			clock := oldStore.Now
			if err := oldStore.Close(); err != nil {
				t.Fatal(err)
			}
			restored, err := store.Open(context.Background(), path, s.NodeID, make([]byte, 32))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.Close() })
			restored.Now = clock
			s.Store = restored
			s.Identity.Store = restored
			s.Definitions.Store = restored
			s.Fault = nil
			d.now = restored.CurrentTime
			req, err = s.Run(context.Background(), req, false)
			if err != nil {
				t.Fatal(err)
			}
			want := "result_unknown"
			calls := 1
			if phase == "before_reserve" || phase == "after_result_save" {
				want = "completed"
			}
			if phase == "after_reserve" {
				calls = 0
			}
			if req.Status != want || d.counts()[req.DownlinkID+":step-a"] != calls {
				t.Fatalf("%s calls=%v", req.Status, d.counts())
			}
			for i := 0; i < 3; i++ {
				if _, err = s.Run(context.Background(), req, false); err != nil {
					t.Fatal(err)
				}
			}
			if d.counts()[req.DownlinkID+":step-a"] != calls {
				t.Fatal("automatic resend", d.counts())
			}
			assertAtomicHistory(t, s, req)
			t.Logf("phase=%s reopened SQLite execution=%s state=%s physical_actions=%d", phase, req.DownlinkID, req.Status, calls)
		})
	}
}
func TestChineseColonIdentityReconcileAndResumeRemainingSteps(t *testing.T) {
	s, users, _ := fixture(t)
	twoSteps(t, s)
	d := deviceEvidence(s)
	d.uncertain = true
	req := queued(t, s, users, "核对:生产线:订单甲")
	req, err := s.Run(context.Background(), req, false)
	if err != nil || req.Status != "result_unknown" {
		t.Fatal(req, err)
	}
	actions, err := s.Store.ExecutionActions(context.Background(), req.DownlinkID)
	if err != nil || len(actions) != 1 {
		t.Fatal(actions, err)
	}
	oldVersion := req.Version
	req, err = s.Reconcile(context.Background(), users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: req.Version, Reason: "设备命令日志已确认完成"})
	if err != nil || req.Status != "ready_to_resume" || req.Steps[0].EvidenceID == "" {
		t.Fatal(req, err)
	}
	if _, err = s.Resume(context.Background(), users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: oldVersion, Reason: "stale screen"}); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	req, err = s.Resume(context.Background(), users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: req.Version, Reason: "继续剩余步骤"})
	if err != nil {
		t.Fatal(err)
	}
	d.uncertain = false
	req, err = s.Run(context.Background(), req, false)
	if err != nil || req.Status != "completed" {
		t.Fatal(req, err)
	}
	if len(req.Steps) != 2 || len(d.counts()) != 2 {
		t.Fatal(req.Steps, d.counts())
	}
	for _, count := range d.counts() {
		if count != 1 {
			t.Fatal(d.counts())
		}
	}
	detail, err := s.Detail(context.Background(), users["engineer"], req.DownlinkID)
	if err != nil || len(detail.Evidence) != 1 || !detail.Evidence[0].Trusted {
		t.Fatal(detail, err)
	}
	assertAtomicHistory(t, s, req)
	t.Logf("Unicode/colon identity=%s original_command=%s evidence=%s final=%s counts=%v", req.DownlinkID, actions[0].CommandID, detail.Evidence[0].ID, req.Status, d.counts())
}
func TestUntrustedFeedbackCannotAuthorizeResume(t *testing.T) {
	cases := map[string]func(*CommandFeedback){
		"missing": func(f *CommandFeedback) { f.Found = false }, "wrong_command": func(f *CommandFeedback) { f.CommandID = "other" }, "wrong_device": func(f *CommandFeedback) { f.DeviceID = "other" }, "wrong_content": func(f *CommandFeedback) { f.PayloadHash = "other" }, "unbound_legacy": func(f *CommandFeedback) { f.Bound = false }, "stale": func(f *CommandFeedback) { f.ObservedMS -= 600000 }, "future": func(f *CommandFeedback) { f.ObservedMS += 600000 }, "missing_source": func(f *CommandFeedback) { f.Source = "" }, "missing_hash": func(f *CommandFeedback) { f.ContentHash = "" }, "unknown": func(f *CommandFeedback) { f.Status = "RESULT_UNKNOWN" },
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			s, users, _ := fixture(t)
			twoSteps(t, s)
			d := deviceEvidence(s)
			d.uncertain = true
			d.alter = alter
			req := queued(t, s, users, name)
			req, err := s.Run(context.Background(), req, false)
			if err != nil {
				t.Fatal(err)
			}
			req, err = s.Reconcile(context.Background(), users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: req.Version, Reason: "核对设备原始反馈"})
			if err != nil || req.Status != "result_unknown" {
				t.Fatal(req, err)
			}
			evidence, err := s.Store.ExecutionEvidence(context.Background(), req.DownlinkID)
			if err != nil || len(evidence) != 1 || evidence[0].Trusted || evidence[0].Rejection == "" {
				t.Fatal(evidence, err)
			}
			if _, err = s.Resume(context.Background(), users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: req.Version, Reason: "continue"}); !errors.Is(err, ErrTransition) {
				t.Fatal(err)
			}
			if d.counts()[req.DownlinkID+":step-a"] != 1 {
				t.Fatal(d.counts())
			}
		})
	}
}
func TestCancelDuringCommandRetainsLateResultAndSkipsRemaining(t *testing.T) {
	s, users, _ := fixture(t)
	twoSteps(t, s)
	d := deviceEvidence(s)
	d.entered = make(chan string, 1)
	d.release = make(chan struct{})
	req := queued(t, s, users, "cancel-active")
	result := make(chan model.Execution, 1)
	fail := make(chan error, 1)
	go func() { r, e := s.Run(context.Background(), req, false); result <- r; fail <- e }()
	<-d.entered
	current, err := s.Get(context.Background(), req.DownlinkID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := s.Cancel(context.Background(), users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: current.Version, Reason: "现场要求停止后续步骤"})
	if err != nil || cancelled.Status != "cancelled_result_unknown" {
		t.Fatal(cancelled, err)
	}
	close(d.release)
	req = <-result
	if err = <-fail; err != nil {
		t.Fatal(err)
	}
	if req.Status != "cancelled" || len(req.Steps) != 1 || req.Steps[0].Status != "SUCCESS" || len(d.counts()) != 1 {
		t.Fatal(req, d.counts())
	}
	evidence, err := s.Store.ExecutionEvidence(context.Background(), req.DownlinkID)
	if err != nil || len(evidence) != 1 || !evidence[0].Trusted || evidence[0].Source != "control.action_journal:edge-a" || evidence[0].ID != req.Steps[0].EvidenceID || evidence[0].CommandID != req.Steps[0].CommandID || evidence[0].ObservedMS != req.Steps[0].FinishedMS {
		t.Fatal("late original result lost its durable evidence", evidence, req, err)
	}
	if _, err = s.Run(context.Background(), req, false); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.Store.ExecutionEvidence(context.Background(), req.DownlinkID)
	if err != nil || store.Hash(unchanged) != store.Hash(evidence) {
		t.Fatal("retry changed late feedback identity", unchanged, err)
	}
	assertAtomicHistory(t, s, req)
	t.Logf("cancelled during command; late SUCCESS retained; next step sends=0; counts=%v", d.counts())
}
func TestConcurrentManualOperationsHaveSingleVersionWinner(t *testing.T) {
	s, users, _ := fixture(t)
	req := queued(t, s, users, "concurrent-manual")
	ready := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-ready
			_, err := s.Cancel(context.Background(), users["engineer"], req.DownlinkID, model.ExecutionAction{ExpectedVersion: req.Version, Reason: "concurrent cancel"})
			results <- err
		}()
	}
	close(ready)
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, store.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
	final, err := s.Get(context.Background(), req.DownlinkID)
	if err != nil || final.Version != req.Version+1 {
		t.Fatal(final, err)
	}
	assertAtomicHistory(t, s, final)
}
func TestHistoricalExecutionAndOperationReadChecksOriginalResources(t *testing.T) {
	s, users, _ := fixture(t)
	ctx := context.Background()
	req := queued(t, s, users, "historical-read")
	_, err := s.Identity.Store.Put(ctx, "control_operation", "history-op", 0, model.ControlOperation{ID: "history-op", ExecutionID: req.DownlinkID, Result: &req, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := s.Store.Get(ctx, "definition", "plan")
	def, _ := store.Decode[model.Definition](doc)
	def.Version++
	def.Policy.Conditions = nil
	def.Policy.Steps[0].DeviceID = "permitted-device"
	if _, err = s.Identity.Store.Put(ctx, "definition", "plan", doc.Version, def); err != nil {
		t.Fatal(err)
	}
	for _, e := range []model.Entity{{ID: "factory", Kind: "asset", Status: "active"}, {ID: "device", Kind: "device", Status: "approved", ParentID: "secret"}, {ID: "permitted-device", Kind: "device", Status: "approved", ParentID: "factory"}} {
		if _, err = s.Identity.Store.Put(ctx, "entity", e.ID, 0, e); err != nil {
			t.Fatal(err)
		}
	}
	reader := users["engineer"]
	reader.User.Resources = []string{"factory"}
	reader.User.Version++
	if _, err = s.Identity.Store.Put(ctx, "user", reader.User.ID, 1, reader.User); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Detail(ctx, reader, req.DownlinkID); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("old resource leaked", err)
	}
	if _, err = s.Operation(ctx, reader, "history-op"); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("old operation result leaked", err)
	}
	// Revoking the reader remains effective even for a cached principal.
	reader.User.Active = false
	reader.User.Version++
	if _, err = s.Identity.Store.Put(ctx, "user", reader.User.ID, 2, reader.User); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Detail(ctx, users["engineer"], req.DownlinkID); !errors.Is(err, identity.ErrDenied) {
		t.Fatal(err)
	}
}

package control

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type dispatchStub struct {
	calls int
	err   error
	after func()
}

func TestUpdatedConfirmationCountsApplyBeforeDispatch(t *testing.T) {
	s, users, dispatch := fixture(t)
	req := approvals(t, s, users, "changed-counts", false)
	policy := s.Store.Policy()
	policy.Confirmations.Engineers = 2
	s.Store.SetPolicy(policy)
	if _, err := s.Dispatch(context.Background(), users["engineer"], req.DownlinkID); err == nil {
		t.Fatal("old approval count bypassed current policy")
	}
	if dispatch.calls != 0 {
		t.Fatal("device called before required confirmations")
	}
}

func (d *dispatchStub) Send(ctx context.Context, step model.Step, id string, deadline int64) (DispatchResult, error) {
	d.calls++
	if d.after != nil {
		d.after()
	}
	if d.err != nil {
		return DispatchResult{}, d.err
	}
	return DispatchResult{Status: "SUCCESS", Message: "device applied"}, nil
}

func TestOrganizationMoveInvalidatesLeaderApproval(t *testing.T) {
	s, users, dispatch := fixture(t)
	ctx := context.Background()
	req := approvals(t, s, users, "organization-moved", false)
	leader := users["leader"].User
	leader.DepartmentID = "unrelated"
	if _, err := s.Store.Put(ctx, "user", leader.ID, 1, leader); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispatch(ctx, users["engineer"], req.DownlinkID); err == nil {
		t.Fatal("moved leader's old approval remained usable")
	}
	if dispatch.calls != 0 {
		t.Fatal("action occurred with invalid leadership")
	}
	leaderPrincipal := users["leader"]
	leaderPrincipal.User = leader
	if _, err := s.Approve(ctx, leaderPrincipal, req.DownlinkID, "leader"); err == nil {
		t.Fatal("unrelated department leader approved request")
	}
}

func TestInterlockIsRecheckedBetweenDeviceSteps(t *testing.T) {
	s, users, dispatch := fixture(t)
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
	d.Policy.Steps = append(d.Policy.Steps, model.Step{ID: "step-b", DeviceID: "device", EdgeID: "edge-a", Action: "actuate", TimeoutMS: 1000})
	if _, err = s.Store.Put(ctx, "definition", d.ID, doc.Version, d); err != nil {
		t.Fatal(err)
	}
	req := approvals(t, s, users, "interlock-between-steps", false)
	req, err = s.Dispatch(ctx, users["engineer"], req.DownlinkID)
	if err != nil {
		t.Fatal(err)
	}
	dispatch.after = func() {
		if dispatch.calls == 1 {
			if _, e := s.Store.Ingest(ctx, store.IngestBatch{MessageID: "trip-between", SourceID: "edge-a", Points: []model.Observation{{DeviceID: "device", Key: "interlock", Value: true, Quality: "GOOD", ObservedMS: s.Store.Now().UnixMilli() + 1}}}); e != nil {
				t.Fatal(e)
			}
		}
	}
	req, err = s.Run(ctx, req, false)
	if err != nil || dispatch.calls != 1 || req.Status == "completed" {
		t.Fatal("interlock did not stop the next device action", req.Status, dispatch.calls, err)
	}
	if len(req.Steps) != 2 || req.Steps[1].Status != "REJECTED" {
		t.Fatal(req.Steps)
	}
}
func fixture(t *testing.T) (*Service, map[string]identity.Principal, *dispatchStub) {
	t.Helper()
	ctx := context.Background()
	s, e := store.Open(ctx, filepath.Join(t.TempDir(), "control.db"), "edge-a", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	auth := &identity.Manager{Store: s, Master: make([]byte, 32)}
	dispatch := &dispatchStub{}
	service := &Service{Store: s, Identity: auth, Definitions: &engine.Service{Store: s}, Dispatcher: dispatch, NodeID: "edge-a", Edge: true}
	users := map[string]identity.Principal{}
	for _, id := range []string{"engineer", "engineer2", "leader", "safety"} {
		role := id
		if id == "engineer2" {
			role = "engineer"
		}
		u := model.User{ID: id, Name: id, Login: id, Roles: []string{role}, Resources: []string{"*"}, Active: true, DepartmentID: "production", Version: 1}
		if _, e = s.Put(ctx, "user", id, 0, u); e != nil {
			t.Fatal(e)
		}
		users[id] = identity.Principal{User: u, Actor: model.Actor{UserID: id, Name: id, Roles: []string{role}}, Local: true, StepUpUntilMS: now.Add(time.Minute).UnixMilli()}
	}
	d := model.Definition{ID: "plan", Name: "Plan", Kind: "strategy", Status: "published", Version: 1, GroupID: "factory", Policy: model.Policy{RiskCategory: "business", RiskLevel: 3, SafetyUserID: "safety", EdgeIDs: []string{"edge-a"}, Conditions: []model.Condition{{DeviceID: "device", Key: "interlock", Operator: "==", Value: false, Interlock: true, MaxAgeMS: 5000}}, Steps: []model.Step{{ID: "step-a", EdgeID: "edge-a", DeviceID: "device", Action: "actuate", TimeoutMS: 1000}}}}
	if _, e = s.Put(ctx, "definition", d.ID, 0, d); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Ingest(ctx, store.IngestBatch{MessageID: "initial", SourceID: "edge-a", Points: []model.Observation{{DeviceID: "device", Key: "interlock", Value: false, ObservedMS: now.UnixMilli(), Quality: "GOOD"}}}); e != nil {
		t.Fatal(e)
	}
	return service, users, dispatch
}
func approvals(t *testing.T, s *Service, users map[string]identity.Principal, id string, override bool) model.Execution {
	t.Helper()
	ctx := context.Background()
	r, e := s.Create(ctx, users["engineer"], "plan", map[string]string{}, override, id)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Dispatch(ctx, users["engineer"], id); e == nil {
		t.Fatal("missing approvals accepted")
	}
	if r, e = s.Approve(ctx, users["engineer"], id, "engineer"); e != nil {
		t.Fatal(e)
	}
	if r, e = s.Approve(ctx, users["leader"], id, "leader"); e != nil {
		t.Fatal(e)
	}
	if override {
		if _, e = s.Dispatch(ctx, users["engineer"], id); e == nil {
			t.Fatal("second engineer not required")
		}
		if r, e = s.Approve(ctx, users["engineer2"], id, "engineer"); e != nil {
			t.Fatal(e)
		}
	}
	return r
}
func TestApprovalExecutionAndRestartDeduplication(t *testing.T) {
	s, users, dispatch := fixture(t)
	ctx := context.Background()
	r := approvals(t, s, users, "request-1", false)
	if r.Status != "approved" {
		t.Fatal(r.Status)
	}
	r, e := s.Dispatch(ctx, users["engineer"], r.DownlinkID)
	if e != nil {
		t.Fatal(e)
	}
	r, e = s.Run(ctx, r, false)
	if e != nil || r.Status != "completed" || dispatch.calls != 1 {
		t.Fatalf("execution %+v %v calls=%d", r, e, dispatch.calls)
	}
	s2 := *s
	r, e = s2.Run(ctx, r, false)
	if e != nil || dispatch.calls != 1 {
		t.Fatal("duplicate physical action after service restart", e, dispatch.calls)
	}
	events, e := s.Store.AuditList(ctx, r.DownlinkID, 100)
	if e != nil || len(events) < 6 {
		t.Fatal("responsibility records missing", len(events), e)
	}
}
func TestUnknownResultNeverResends(t *testing.T) {
	s, users, dispatch := fixture(t)
	ctx := context.Background()
	dispatch.err = errors.New("connection lost after transmission")
	r := approvals(t, s, users, "uncertain", false)
	r, e := s.Dispatch(ctx, users["engineer"], r.DownlinkID)
	if e != nil {
		t.Fatal(e)
	}
	r, e = s.Run(ctx, r, false)
	if e != nil || r.Status != "result_unknown" || dispatch.calls != 1 {
		t.Fatalf("uncertain %+v %v", r, e)
	}
	if _, e = s.Run(ctx, r, false); e != nil {
		t.Fatal(e)
	}
	if dispatch.calls != 1 {
		t.Fatal("uncertain non-idempotent action resent")
	}
}
func TestExpiredAndRevokedApprovalsAndInterlock(t *testing.T) {
	for _, scenario := range []string{"expired", "revoked", "interlocked", "old-version"} {
		t.Run(scenario, func(t *testing.T) {
			s, users, dispatch := fixture(t)
			ctx := context.Background()
			r := approvals(t, s, users, scenario, false)
			switch scenario {
			case "expired":
				now := s.Store.Now().Add(6 * time.Minute)
				s.Store.Now = func() time.Time { return now }
			case "revoked":
				u := users["leader"].User
				u.Active = false
				s.Store.Put(ctx, "user", u.ID, 1, u)
			case "interlocked":
				s.Store.Ingest(ctx, store.IngestBatch{MessageID: "trip", SourceID: "edge-a", Points: []model.Observation{{DeviceID: "device", Key: "interlock", Value: true, ObservedMS: s.Store.Now().UnixMilli(), Quality: "GOOD"}}})
			case "old-version":
				doc, _ := s.Store.Get(ctx, "definition", "plan")
				d, _ := store.Decode[model.Definition](doc)
				d.Version++
				s.Store.Put(ctx, "definition", "plan", doc.Version, d)
			}
			result, e := s.Dispatch(ctx, users["engineer"], r.DownlinkID)
			if e == nil && result.Status != "rejected" {
				t.Fatalf("unsafe dispatch accepted: %+v", result)
			}
			if dispatch.calls != 0 {
				t.Fatal("physical action occurred")
			}
		})
	}
}
func TestSafetyOverrideRequiresDesignatedLocalSecondFactor(t *testing.T) {
	s, users, _ := fixture(t)
	ctx := context.Background()
	doc, _ := s.Store.Get(ctx, "definition", "plan")
	d, _ := store.Decode[model.Definition](doc)
	d.Policy.RiskCategory = "safety"
	d.Version = 2
	s.Store.Put(ctx, "definition", "plan", 1, d)
	r := approvals(t, s, users, "safety-request", true)
	if r.Status == "approved" {
		t.Fatal("safety approval bypassed")
	}
	remote := users["safety"]
	remote.Local = false
	if _, e := s.Approve(ctx, remote, r.DownlinkID, "safety"); e == nil {
		t.Fatal("remote safety approval accepted")
	}
	expired := users["safety"]
	expired.StepUpUntilMS = 0
	if _, e := s.Approve(ctx, expired, r.DownlinkID, "safety"); e == nil {
		t.Fatal("missing second factor accepted")
	}
	r, e := s.Approve(ctx, users["safety"], r.DownlinkID, "safety")
	if e != nil || r.Status != "approved" {
		t.Fatalf("local safety %+v %v", r, e)
	}
}

package control

import (
	"context"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestCurrentAuthorizationAndResourceVersionsBeforeExecution(t *testing.T) {
	for _, scenario := range []string{"device_moved", "team_removed", "grant_changed", "principal_disabled", "session_revoked", "reference_changed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, users, dispatch := fixture(t)
			for _, entity := range []model.Entity{{ID: "factory", Kind: "asset"}, {ID: "elsewhere", Kind: "asset"}, {ID: "device", Kind: "device", ParentID: "factory"}} {
				if _, err := s.Identity.Store.Put(ctx, "entity", entity.ID, 0, entity); err != nil {
					t.Fatal(err)
				}
			}
			engineer := users["engineer"].User
			engineer.Resources = nil
			engineer.Teams = []string{"operators"}
			if _, err := s.Identity.Store.Put(ctx, "user", engineer.ID, 1, engineer); err != nil {
				t.Fatal(err)
			}
			users["engineer"] = identity.Principal{User: engineer, Actor: users["engineer"].Actor}
			grant := identity.Grant{ID: "operating-team", TeamID: "operators", GroupID: "factory", Actions: []string{"read", "control", "approve"}}
			if _, err := s.Identity.Store.Put(ctx, "grant", grant.ID, 0, grant); err != nil {
				t.Fatal(err)
			}
			req := approvals(t, s, users, "authorization:"+scenario, false)
			p := users["engineer"]
			switch scenario {
			case "device_moved":
				if _, err := s.Identity.Store.Put(ctx, "entity", "device", 1, model.Entity{ID: "device", Kind: "device", ParentID: "elsewhere"}); err != nil {
					t.Fatal(err)
				}
			case "team_removed", "principal_disabled":
				if scenario == "team_removed" {
					engineer.Teams = nil
				} else {
					engineer.Active = false
				}
				if _, err := s.Identity.Store.Put(ctx, "user", engineer.ID, 2, engineer); err != nil {
					t.Fatal(err)
				}
			case "grant_changed":
				grant.GroupID = "elsewhere"
				if _, err := s.Identity.Store.Put(ctx, "grant", grant.ID, 1, grant); err != nil {
					t.Fatal(err)
				}
			case "session_revoked":
				p.SessionDocument, p.SessionVersion = "session-engineer", 1
				if _, err := s.Identity.Store.Put(ctx, "session", p.SessionDocument, 0, identity.Session{ID: "session-engineer", UserID: engineer.ID, ExpiresMS: s.Store.CurrentTime().Add(-time.Second).UnixMilli()}); err != nil {
					t.Fatal(err)
				}
			case "reference_changed":
				doc, _ := s.Store.Get(ctx, "definition", "plan")
				definition, _ := store.Decode[model.Definition](doc)
				definition.Version++
				definition.Policy.Steps[0].Params = map[string]string{"value": "changed"}
				if _, err := s.Identity.Store.Put(ctx, "definition", definition.ID, doc.Version, definition); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.Dispatch(ctx, p, req.DownlinkID)
			if err == nil && result.Status != "rejected" || dispatch.calls != 0 {
				t.Fatal("changed authorization permitted execution", result, err, dispatch.calls)
			}
			t.Logf("current %s denied before device send; physical_actions=0", scenario)
		})
	}
}

func TestInterventionRequiresHistoricalConditionResources(t *testing.T) {
	ctx := context.Background()
	s, users, _ := fixture(t)
	initial, _ := s.Store.Get(ctx, "definition", "plan")
	initialDefinition, _ := store.Decode[model.Definition](initial)
	initialDefinition.Version++
	initialDefinition.Policy.Conditions[0].DeviceID = "sensor-original"
	if _, err := s.Identity.Store.Put(ctx, "definition", "plan", initial.Version, initialDefinition); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Identity.Store.Ingest(ctx, store.IngestBatch{MessageID: "original-condition", SourceID: "edge-a", Points: []model.Observation{{DeviceID: "sensor-original", Key: "interlock", Value: false, ObservedMS: s.Store.CurrentTime().UnixMilli(), Quality: "GOOD"}}}); err != nil {
		t.Fatal(err)
	}
	req := queued(t, s, users, "historical-condition")
	d := deviceEvidence(s)
	d.uncertain = true
	req, err := s.Run(ctx, req, false)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := s.Store.Get(ctx, "definition", "plan")
	definition, _ := store.Decode[model.Definition](doc)
	definition.Version++
	definition.Policy.Conditions = nil
	if _, err = s.Identity.Store.Put(ctx, "definition", definition.ID, doc.Version, definition); err != nil {
		t.Fatal(err)
	}
	u := users["engineer"].User
	u.Resources = []string{"factory", "device"}
	if _, err = s.Identity.Store.Put(ctx, "user", u.ID, 1, u); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SubmitOperation(ctx, users["engineer"], req.DownlinkID, "reconcile", model.ExecutionAction{ExpectedVersion: req.Version, OperationID: "old-condition-reconcile", Reason: "检查历史条件资源授权"}); err == nil {
		t.Fatal("historical condition and snapshot resources were exposed")
	}
	if len(d.counts()) != 1 {
		t.Fatal(d.counts())
	}
}

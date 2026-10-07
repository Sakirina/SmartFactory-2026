package cloudsync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func syncBusinessFixture(t *testing.T) (*fixture, *application.Business, *application.Business, identity.Principal, identity.Principal, model.Alarm) {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	f.entity.ParentID = "factory"
	f.entity.Version++
	if _, e := f.cloud.Put(ctx, "entity", f.entity.ID, f.entity.Version-1, f.entity); e != nil {
		t.Fatal(e)
	}
	if _, e := f.cloud.Put(ctx, "department", "operations", 0, map[string]any{"id": "operations", "version": 1}); e != nil {
		t.Fatal(e)
	}
	doc, _ := f.cloud.Get(ctx, "user", "engineer")
	u, _ := store.Decode[model.User](doc)
	u.DepartmentID = "operations"
	u.Version++
	if _, e := f.cloud.Put(ctx, "user", u.ID, doc.Version, u); e != nil {
		t.Fatal(e)
	}
	if _, e := f.server.Identity.CreateUser(ctx, model.Actor{}, model.User{ID: "second", Name: "Second", Login: "second", Active: true, Roles: []string{"engineer"}, Resources: []string{"factory"}, DepartmentID: "operations"}, "simulated-password", "", 0); e != nil {
		t.Fatal(e)
	}
	d := model.Definition{ID: "business-sync-rule", Name: "同期规则", Kind: "alarm", GroupID: "factory", Version: 1, Status: "published", SchemaVersion: model.ContractVersion, Selector: model.Selector{DeviceIDs: []string{"counter"}, Keys: []string{"value"}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "alarm", Type: "alarm"}}, Connections: []model.Connection{{From: "input", To: "alarm"}}}
	if _, e := f.cloud.Put(ctx, "definition", d.ID, 0, d); e != nil {
		t.Fatal(e)
	}
	if e := f.client.Exchange(ctx); e != nil {
		t.Fatal(e)
	}
	_, cp, e := f.server.Identity.Login(ctx, "engineer", "simulated-password", "", false, "test-cloud")
	if e != nil {
		t.Fatal(e)
	}
	_, ep, e := f.client.Identity.Login(ctx, "engineer", "simulated-password", "", true, "test-edge")
	if e != nil {
		t.Fatal(e)
	}
	a := model.Alarm{ID: "shared-business-alarm", DefinitionID: d.ID, DefinitionVersion: 1, EntityID: "counter", Severity: "MAJOR", Active: true, StartedMS: time.Now().UnixMilli(), UpdatedMS: time.Now().UnixMilli(), Version: 1, Count: 1}
	for _, db := range []*store.Store{f.cloud, f.edge} {
		if e := db.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral("alarm", a.ID, a) }); e != nil {
			t.Fatal(e)
		}
	}
	cloud := &application.Business{Store: f.cloud, Identity: f.server.Identity, NodeID: f.cloud.NodeID, Mode: "cloud"}
	edge := &application.Business{Store: f.edge, Identity: f.client.Identity, NodeID: f.edge.NodeID, Mode: "edge"}
	return f, cloud, edge, cp, ep, a
}

func syncAction(t *testing.T, s *application.Business, p identity.Principal, a model.Alarm, request, action, assignee string) model.AlarmDetail {
	t.Helper()
	ctx := context.Background()
	d, e := s.AlarmDetail(ctx, p, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	d, e = s.ActOnAlarm(ctx, p, a.ID, application.AlarmActionInput{RequestID: request, ExpectedVersion: d.Case.Version, ExpectedActionVersion: d.ActionVersion, Action: action, AssigneeID: assignee, Reason: "同步现场处置"})
	if e != nil {
		t.Fatal(e)
	}
	return d
}

func TestBusinessAlarmMTLSConcurrentAssignmentConverges(t *testing.T) {
	f, cloud, edge, cp, ep, a := syncBusinessFixture(t)
	ctx := context.Background()
	syncAction(t, cloud, cp, a, "cloud-assign", "assign", "engineer")
	syncAction(t, edge, ep, a, "edge-assign", "assign", "second")
	if e := f.client.Exchange(ctx); e != nil {
		t.Fatal(e)
	}
	for _, db := range []*store.Store{f.cloud, f.edge} {
		c, e := db.AlarmCase(ctx, a)
		if e != nil || c.Status != "conflict" || len(c.ConflictFields) != 1 || len(c.Operations) != 2 {
			t.Fatal(c, e)
		}
	}
	syncAction(t, cloud, cp, a, "resolve-assignment", "assign", "engineer")
	if e := f.client.Exchange(ctx); e != nil {
		t.Fatal(e)
	}
	syncAction(t, edge, ep, a, "edge-note", "note", "")
	syncAction(t, cloud, cp, a, "cloud-ack", "acknowledge", "")
	if e := f.client.Exchange(ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.client.Exchange(ctx); e != nil {
		t.Fatal(e)
	}
	var expected string
	for _, db := range []*store.Store{f.cloud, f.edge} {
		c, e := db.AlarmCase(ctx, a)
		if e != nil || c.AssigneeID != "engineer" || c.Status != "open" || !c.Acknowledged || len(c.Operations) != 5 {
			t.Fatal(c, e)
		}
		hash := store.Hash(c.Operations)
		if expected != "" && expected != hash {
			t.Fatal("operation sets differ")
		}
		expected = hash
		if issues, e := db.VerifyAudit(ctx); e != nil || len(issues) > 0 {
			t.Fatal(issues, e)
		}
	}
	t.Log("real mutual-TLS exchange retains both concurrent assignments, exposes conflict, converges after reviewed reassignment, and retains cloud acknowledgement plus edge note with deduplicated audit")
}

func TestBusinessAlarmSyncRejectsIdentityAndRevokedActor(t *testing.T) {
	f, _, edge, _, ep, a := syncBusinessFixture(t)
	ctx := context.Background()
	d := syncAction(t, edge, ep, a, "edge-only-note", "note", "")
	op := d.Case.Operations[0]
	makeChange := func(v model.AlarmOperation) store.Change {
		raw, _ := json.Marshal(v)
		return store.Change{Document: store.Document{Kind: "alarm_operation", ID: v.ID, Version: v.Version, UpdatedMS: v.AtMS, Data: raw}}
	}
	for _, mutate := range []func(*model.AlarmOperation){func(v *model.AlarmOperation) { v.SourceID = "different-edge" }, func(v *model.AlarmOperation) { v.EntityID = "not-owned" }, func(v *model.AlarmOperation) { v.Actor.AI = true }} {
		bad := op
		mutate(&bad)
		if e := f.server.importChanges(ctx, "edge-a", []store.Change{makeChange(bad)}, true); e == nil {
			t.Fatal("forged operation accepted", bad)
		}
	}
	if e := f.server.importChanges(ctx, "edge-a", []store.Change{makeChange(op)}, true); e != nil {
		t.Fatal(e)
	}
	changed := op
	changed.Reason = "same-id-different-content"
	if e := f.server.importChanges(ctx, "edge-a", []store.Change{makeChange(changed)}, true); !errors.Is(e, store.ErrConflict) {
		t.Fatal(e)
	}
	changed = op
	changed.ID = "reused-source-sequence"
	if e := f.server.importChanges(ctx, "edge-a", []store.Change{makeChange(changed)}, true); !errors.Is(e, store.ErrConflict) {
		t.Fatal(e)
	}
	next := syncAction(t, edge, ep, a, "revoked-note", "note", "")
	op = next.Case.Operations[len(next.Case.Operations)-1]
	doc, _ := f.cloud.Get(ctx, "user", "engineer")
	u, _ := store.Decode[model.User](doc)
	u.Active = false
	u.Version++
	if _, e := f.cloud.Put(ctx, "user", u.ID, doc.Version, u); e != nil {
		t.Fatal(e)
	}
	if e := f.server.importChanges(ctx, "edge-a", []store.Change{makeChange(op)}, true); !errors.Is(e, identity.ErrDenied) {
		t.Fatal("revoked actor imported", e)
	}
	t.Log("source certificate identity, device ownership, AI restriction, immutable content, source sequence and current actor revocation enforced on synchronized operations")
}

type acceptConnector struct{}

func (acceptConnector) ApplyConnectorConfiguration(context.Context, model.ConnectorConfiguration, json.RawMessage) error {
	return nil
}
func TestBusinessConnectorPublicConfigurationMTLSSynchronization(t *testing.T) {
	f, _, edge, _, ep, _ := syncBusinessFixture(t)
	ctx := context.Background()
	input := application.SaveConnectorConfigurationInput{RequestID: "synchronized-config", GroupID: "factory", EdgeID: "edge-a", Protocol: "mqtt_device", Parameters: deviceconfig.Parameters{Kind: "connector", ConnectorID: "synchronized", Connection: map[string]any{"url": "tcp://localhost:1883", "password": "private-connector-password"}, Converter: map[string]any{"action_mappings": map[string]any{"switch": map[string]any{"topic": "command/topic"}}}}}
	detail, e := edge.SaveConnectorConfiguration(ctx, ep, input)
	if e != nil {
		t.Fatal(e)
	}
	if e = edge.ApplyConnectorConfiguration(ctx, detail.Configuration, acceptConnector{}); e != nil {
		t.Fatal(e)
	}
	if e = f.client.Exchange(ctx); e != nil {
		t.Fatal(e)
	}
	cdoc, e := f.cloud.Get(ctx, "connector_configuration", detail.Configuration.ID)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(cdoc.Data), "private-connector-password") {
		t.Fatal("private password leaked")
	}
	if _, e = f.cloud.Get(ctx, "connector_configuration_secret", detail.Configuration.CredentialRef); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("private secret synchronized", e)
	}
	doc, e := f.cloud.Get(ctx, "connector_configuration_receipt", detail.Configuration.ID)
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := store.Decode[model.ConnectorConfigurationReceipt](doc)
	if e != nil || receipt.Status != "applied" || receipt.ConfigurationVersion != 1 || receipt.SourceID != "edge-a" {
		t.Fatal(receipt, e)
	}
	t.Log("real mTLS exchange synchronizes public connector config and exact application receipt while encrypted secret versions remain on the owner")
}

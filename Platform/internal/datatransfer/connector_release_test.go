package datatransfer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"google.golang.org/protobuf/encoding/protojson"
)

// Uses an actual separate ConfigManager process, its persistent journal and
// generated gRPC consumer observation. No consumer response is mocked.
func TestControlledFixedConnectorReleaseRollbackAndDelayedBusinessTask(t *testing.T) {
	address := os.Getenv("SF_TEST_DATATRANSFER_CONNECTOR_ADDRESS")
	if address == "" {
		t.Skip("actual DataTransfer fixture address required")
	}
	ctx := context.Background()
	db, e := store.Open(ctx, filepath.Join(t.TempDir(), "edge.db"), "bridge-edge", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	auth := &identity.Manager{Store: db, Master: make([]byte, 32)}
	if _, e = auth.CreateUser(ctx, model.Actor{UserID: "installation", Source: "fixture"}, model.User{ID: "admin", Login: "admin", Name: "管理员", Active: true, Roles: []string{"admin"}, Resources: []string{"*"}}, "configuration-fixture-password", "", 0); e != nil {
		t.Fatal(e)
	}
	_, principal, e := auth.Login(ctx, "admin", "configuration-fixture-password", "", false, "configuration-fixture")
	if e != nil {
		t.Fatal(e)
	}
	for _, entity := range []model.Entity{{ID: "factory", Kind: "asset", Name: "验证工厂", Status: "active", Version: 1}, {ID: "bridge-edge", Kind: "edge", ParentID: "factory", Name: "配置节点", Status: "active", Version: 1}} {
		if _, e = db.Put(ctx, "entity", entity.ID, 0, entity); e != nil {
			t.Fatal(e)
		}
	}
	business := &application.Business{Store: db, Identity: auth, Mode: "edge", NodeID: db.NodeID}
	input := application.SaveConnectorConfigurationInput{RequestID: "first", ExpectedVersion: 0, GroupID: "factory", EdgeID: db.NodeID, Protocol: "mqtt_device", Parameters: deviceconfig.Parameters{Kind: "connector", ConnectorID: "release-rollback", Connection: map[string]any{"url": "tcp://127.0.0.1:1883", "username": "first", "password": "first-private"}, Polling: map[string]any{"interval_millis": 1000}}}
	first, e := business.SaveConnectorConfiguration(ctx, principal, input)
	if e != nil {
		t.Fatal(e)
	}
	bridge, e := Open(db, db.NodeID, address, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer bridge.Close()
	cfg := &configcenter.Service{Store: db, Identity: auth, ReadConnectorRuntime: bridge.ReadConnectorRuntime, RemoveConnectorRuntime: bridge.RemoveReleaseConnectorConfiguration}
	payload := func(c model.ConnectorConfiguration) json.RawMessage {
		d, e := db.Get(ctx, "connector_configuration_secret", c.CredentialRef)
		if e != nil {
			t.Fatal(e)
		}
		var secret struct {
			Ciphertext string `json:"ciphertext"`
		}
		if e = store.DecodeJSON(d.Data, &secret); e != nil {
			t.Fatal(e)
		}
		raw, e := auth.Decrypt("connector:"+c.CredentialRef, secret.Ciphertext)
		if e != nil {
			t.Fatal(e)
		}
		return json.RawMessage(raw)
	}
	applyRelease := func(c model.ConnectorConfiguration) model.ConfigurationReference {
		ref := model.ConfigurationReference{Kind: "connector", ID: c.ID, Version: c.Version, Digest: configcenter.ConnectorDigest(c)}
		envelope := model.ConfigurationEnvelope{Reference: ref, NodeID: db.NodeID, Program: "edge", Purpose: "release-runtime", Dynamic: true, Connector: &c, CredentialRef: c.CredentialRef}
		if e = cfg.PinLocalRelease(ctx, []model.ConfigurationEnvelope{envelope}); e != nil {
			t.Fatal(e)
		}
		subscriber := configcenter.Subscriber{NodeID: db.NodeID, Local: cfg, ApplyConnector: bridge.ApplyReleaseConnectorConfiguration}
		if e = subscriber.ApplyEnvelopes(ctx, []model.ConfigurationEnvelope{envelope}, map[string]json.RawMessage{c.CredentialRef: payload(c)}, true); e != nil {
			t.Fatal(e)
		}
		return ref
	}
	firstRef := applyRelease(first.Configuration)
	initialGeneration, initialDigest, e := bridge.ReadConnectorRuntime(ctx, firstRef)
	if e != nil {
		t.Fatal(e)
	}
	input.RequestID = "second"
	input.ExpectedVersion = 1
	input.Parameters.Connection = map[string]any{"url": "tcp://127.0.0.1:1884", "username": "second", "password": "second-private"}
	second, e := business.SaveConnectorConfiguration(ctx, principal, input)
	if e != nil {
		t.Fatal(e)
	}
	if e = business.ApplyConnectorConfiguration(ctx, second.Configuration, bridge); !errors.Is(e, store.ErrConflict) {
		t.Fatal("ordinary saved version replaced fixed release", e)
	}
	sameGeneration, sameDigest, e := bridge.ReadConnectorRuntime(ctx, firstRef)
	if e != nil || sameGeneration != initialGeneration || sameDigest != initialDigest {
		t.Fatal("fixed consumer changed", e)
	}
	// The external collector succeeds, then the platform write fails. The next
	// different content must read the collector generation and receive a new one.
	if _, e = db.DB.ExecContext(ctx, `CREATE TRIGGER reject_connector_runtime BEFORE UPDATE ON documents WHEN NEW.kind='connector_runtime_application' BEGIN SELECT RAISE(FAIL,'isolated platform persistence failure'); END`); e != nil {
		t.Fatal(e)
	}
	secondEnvelope := model.ConfigurationEnvelope{Reference: model.ConfigurationReference{Kind: "connector", ID: second.Configuration.ID, Version: second.Configuration.Version, Digest: configcenter.ConnectorDigest(second.Configuration)}, NodeID: db.NodeID, Connector: &second.Configuration}
	if e = cfg.PinLocalRelease(ctx, []model.ConfigurationEnvelope{secondEnvelope}); e != nil {
		t.Fatal(e)
	}
	if e = bridge.ApplyReleaseConnectorConfiguration(ctx, second.Configuration, payload(second.Configuration)); e == nil {
		t.Fatal("platform persistence fault was not injected")
	}
	if _, e = db.DB.ExecContext(ctx, `DROP TRIGGER reject_connector_runtime`); e != nil {
		t.Fatal(e)
	}
	if _, e = cfg.RuntimeSnapshot(ctx); e == nil {
		t.Fatal("unrecorded external success was reported as old running content")
	}
	applyRelease(first.Configuration)
	applyRelease(first.Configuration)
	if _, _, e = bridge.ReadConnectorRuntime(ctx, firstRef); e != nil {
		t.Fatal("different content after unrecorded external success was not adopted", e)
	}
	secondRef := applyRelease(second.Configuration)
	secondGeneration, secondDigest, e := bridge.ReadConnectorRuntime(ctx, secondRef)
	if e != nil || secondGeneration <= initialGeneration || secondDigest == initialDigest {
		t.Fatal("new fixed content not adopted", e)
	}
	input.RequestID = "third-secret-only"
	input.ExpectedVersion = 2
	input.Parameters.Connection["password"] = "third-private"
	third, e := business.SaveConnectorConfiguration(ctx, principal, input)
	if e != nil {
		t.Fatal(e)
	}
	thirdRef := applyRelease(third.Configuration)
	thirdGeneration, thirdDigest, e := bridge.ReadConnectorRuntime(ctx, thirdRef)
	if e != nil || thirdGeneration <= secondGeneration || thirdDigest != secondDigest {
		t.Fatal("secret-only update did not keep public digest and advance authenticated application identity", e)
	}
	var oldPrivate dt.DeviceConfigUpdate
	if e = protojson.Unmarshal(payload(second.Configuration), &oldPrivate); e != nil {
		t.Fatal(e)
	}
	wrongSecret, e := bridge.Client.GetConnectorConfiguration(ctx, &dt.ConnectorConfigurationRequest{ConnectorId: second.Configuration.ConnectorID, ExpectedConfiguration: oldPrivate.GetConnectorConfig()})
	if e != nil || wrongSecret.MatchesExpected {
		t.Fatal("consumer observation accepted old secret with equal public configuration", e)
	}
	applyRelease(first.Configuration)
	rollbackGeneration, rollbackDigest, e := bridge.ReadConnectorRuntime(ctx, firstRef)
	if e != nil || rollbackGeneration <= thirdGeneration || rollbackDigest != initialDigest {
		t.Fatal("authorized source rollback did not use new application generation", e)
	}
	if e = business.ApplyConnectorConfiguration(ctx, second.Configuration, bridge); e != nil {
		t.Fatal("delayed older business task was not ignored", e)
	}
	if e = business.ApplyConnectorConfiguration(ctx, third.Configuration, bridge); !errors.Is(e, store.ErrConflict) {
		t.Fatal("latest ordinary task changed fixed rollback", e)
	}
	var delayed dt.DeviceConfigUpdate
	if e = protojson.Unmarshal(payload(second.Configuration), &delayed); e != nil {
		t.Fatal(e)
	}
	delayed.UpdateId = "late-direct-original-second-version"
	delayed.EntityRevision = second.Configuration.Version
	response, e := bridge.Client.PushDeviceConfig(ctx, &delayed)
	if e != nil || !response.Success || response.AppliedEntityRevision != rollbackGeneration {
		t.Fatal("ConfigManager lost out-of-order protection", e, response)
	}
	generation, digest, e := bridge.ReadConnectorRuntime(ctx, firstRef)
	if e != nil || generation != rollbackGeneration || digest != rollbackDigest {
		t.Fatal("late old update altered actual rollback consumer", e)
	}
	runtime, e := cfg.RuntimeSnapshot(ctx)
	if e != nil || len(runtime) != 1 || runtime[0].Reference != firstRef || runtime[0].ApplyGeneration != rollbackGeneration || runtime[0].ConsumerDigest != rollbackDigest {
		t.Fatal("runtime did not observe actual ConfigManager", e)
	}
	// A direct, newer collector change is detected by the formal runtime query.
	delayed.UpdateId = "external-consumer-drift"
	delayed.EntityRevision = rollbackGeneration + 1
	if response, e = bridge.Client.PushDeviceConfig(ctx, &delayed); e != nil || !response.Success {
		t.Fatal(e, response)
	}
	if _, e = cfg.RuntimeSnapshot(ctx); e == nil {
		t.Fatal("actual consumer drift was reported as running")
	}
	applyRelease(first.Configuration)
	ordinary := model.ConfigurationEnvelope{Reference: model.ConfigurationReference{Kind: "parameter", ID: "ordinary.retained", Version: 1, Digest: store.Hash("ordinary")}, NodeID: db.NodeID, Value: 7}
	if e = db.Write(ctx, func(tx *store.Tx) error {
		return tx.SetEphemeral("configuration_runtime", "parameter:ordinary.retained", ordinary)
	}); e != nil {
		t.Fatal(e)
	}
	if e = cfg.PinLocalRelease(ctx, []model.ConfigurationEnvelope{}); e != nil {
		t.Fatal(e)
	}
	removed, e := bridge.Client.GetConnectorConfiguration(ctx, &dt.ConnectorConfigurationRequest{ConnectorId: first.Configuration.ConnectorID})
	if e != nil || removed.Found {
		t.Fatal("previous release connector consumer survived removal", e)
	}
	if _, e = db.Get(ctx, "configuration_runtime", "connector:"+first.Configuration.ID); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("old release runtime survived removal", e)
	}
	if _, e = db.Get(ctx, "configuration_runtime", "parameter:ordinary.retained"); e != nil {
		t.Fatal("ordinary runtime was removed with previous release", e)
	}
	t.Logf("formal Business save + real generated gRPC ConfigManager: fixed v1 gen%d; ordinary v2 held; explicit v2 gen%d; authorized v1 rollback gen%d; delayed original v2 held; actual consumer drift rejected", initialGeneration, secondGeneration, rollbackGeneration)
}

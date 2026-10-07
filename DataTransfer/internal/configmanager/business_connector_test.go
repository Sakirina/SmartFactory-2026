package configmanager

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	"competition2026/product/datatransfer/internal/state"
	"google.golang.org/protobuf/proto"
)

func TestBusinessConnectorVersionPayloadAndDelayedOldTask(t *testing.T) {
	const protocol = "business_connector_fake"
	connector.Register(protocol, func() connector.Connector { return &cfgFakeConnector{} })
	manager, e := connector.NewManager([]config.ConnectorConfig{}, &cfgPublisher{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	journal, e := state.Open(context.Background(), filepath.Join(t.TempDir(), "connector-state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer journal.Close()
	cm := New(manager, nil)
	if e = cm.AttachJournal(context.Background(), journal); e != nil {
		t.Fatal(e)
	}
	update := func(id string, version int64, password string) *dt.DeviceConfigUpdate {
		raw, _ := json.Marshal(map[string]any{"url": "tls://localhost:8883", "password": password, "tls": map[string]any{"enabled": true, "insecure_skip_verify": true, "cert_file": "client.crt", "key_file": "client.key", "ca_file": "ca.crt"}})
		return &dt.DeviceConfigUpdate{UpdateId: id, EntityRevision: version, Action: dt.DeviceConfigUpdate_UPDATE_CONNECTOR, Config: &dt.DeviceConfigUpdate_ConnectorConfig{ConnectorConfig: &dt.ConnectorConfigPayload{ConnectorId: "test", Protocol: protocol, Connection: raw, ReportStrategy: &dt.ReportStrategyConfig{Mode: dt.ReportStrategyMode_ON_REPORT_PERIOD, PeriodSeconds: 7}}}}
	}
	one, two := update("connector-config:edge-a/test:1", 1, "first-private-value"), update("connector-config:edge-a/test:2", 2, "second-private-value")
	if r := cm.Apply(two); !r.Success || r.AppliedEntityRevision != 2 {
		t.Fatal(r)
	}
	if r := cm.Apply(one); !r.Success || r.AppliedEntityRevision != 2 || r.ErrorMessage == "" {
		t.Fatal("delayed old revision", r)
	}
	cfg, ok := manager.ConnectorConfig("test")
	if !ok || cfg.Connection.Password != "second-private-value" || !cfg.Connection.TLS.Enabled || !cfg.Connection.TLS.InsecureSkipVerify || cfg.Connection.TLS.KeyFile != "client.key" || cfg.Connection.TLS.CertFile != "client.crt" || cfg.Connection.TLS.CAFile != "ca.crt" || cfg.ReportStrategy.PeriodSeconds != 7 {
		t.Fatal("effective connector did not retain newer payload and snake_case TLS references")
	}
	changed := proto.Clone(two).(*dt.DeviceConfigUpdate)
	changed.GetConnectorConfig().Connection = []byte(`{"password":"different"}`)
	if r := cm.Apply(changed); r.Success {
		t.Fatal("identity reused with different payload")
	}
	restarted, e := connector.NewManager([]config.ConnectorConfig{}, &cfgPublisher{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	cm = New(restarted, nil)
	if e = cm.AttachJournal(context.Background(), journal); e != nil {
		t.Fatal(e)
	}
	if r := cm.Apply(one); !r.Success || r.AppliedEntityRevision != 2 {
		t.Fatal("persistent old receipt", r)
	}
	cfg, ok = restarted.ConnectorConfig("test")
	if !ok || cfg.Connection.Password != "second-private-value" {
		t.Fatal("restart restored old payload")
	}
	t.Log("real ConfigManager keeps revision 2 password/TLS/report strategy when revision 1 arrives late; conflicting identity rejected; effective configuration and receipts survive restart")
}

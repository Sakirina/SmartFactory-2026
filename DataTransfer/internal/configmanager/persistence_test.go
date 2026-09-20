package configmanager

import (
	"context"
	"path/filepath"
	"testing"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	"competition2026/product/datatransfer/internal/state"
)

func TestConfigurationAndRevisionRestoreAcrossRestart(t *testing.T) {
	const protocol = "persist_fake"
	connector.Register(protocol, func() connector.Connector { return &cfgFakeConnector{} })
	path := filepath.Join(t.TempDir(), "state.db")
	journal, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := connector.NewManager([]config.ConnectorConfig{{ConnectorID: "connection", Protocol: protocol}}, &cfgPublisher{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := New(manager, nil)
	if err := m.AttachJournal(context.Background(), journal); err != nil {
		t.Fatal(err)
	}
	update := &dtv1.DeviceConfigUpdate{UpdateId: "version-7", EntityRevision: 7, Action: dtv1.DeviceConfigUpdate_ADD_DEVICE, Config: &dtv1.DeviceConfigUpdate_DeviceConfig{DeviceConfig: &dtv1.DeviceConfigPayload{ConnectorId: "connection", DeviceId: "device", DeviceName: "revision seven"}}}
	if response := m.Apply(update); !response.Success {
		t.Fatal(response)
	}
	_ = journal.Close()
	journal, err = state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	manager, err = connector.NewManager(nil, &cfgPublisher{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m = New(manager, nil)
	if err := m.AttachJournal(context.Background(), journal); err != nil {
		t.Fatal(err)
	}
	if _, found := manager.ResolveDevice("device"); !found {
		t.Fatal("device configuration was not restored")
	}
	if response := m.Apply(update); !response.Success {
		t.Fatal(response)
	}
	update.UpdateId, update.EntityRevision = "old-version-6", 6
	update.GetDeviceConfig().DeviceName = "old value"
	if response := m.Apply(update); !response.Success {
		t.Fatal(response)
	}
	cfg, _ := manager.ConnectorConfig("connection")
	if len(cfg.Devices) != 1 || cfg.Devices[0].DeviceName != "revision seven" {
		t.Fatalf("old revision replaced restored state: %+v", cfg)
	}
	update.UpdateId = "version-7"
	if response := m.Apply(update); response.Success {
		t.Fatal("conflicting update_id accepted")
	}
}

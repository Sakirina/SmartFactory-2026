package configmanager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
)

func TestConfigManagerAppliesDeviceUpdatesAndProtectsRevision(t *testing.T) {
	const protocol = "cfg_fake"
	connector.Register(protocol, func() connector.Connector { return &cfgFakeConnector{} })
	manager, err := connector.NewManager([]config.ConnectorConfig{{ConnectorID: "conn-1", Protocol: protocol}}, &cfgPublisher{}, nil)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	cfgManager := New(manager, nil)
	datapoints, _ := json.Marshal([]config.DatapointConfig{{Key: "temperature", Source: "temp", DataType: "float64"}})
	update := &dtv1.DeviceConfigUpdate{
		UpdateId: "update-1",
		Action: dtv1.DeviceConfigUpdate_ADD_DEVICE,
		EntityRevision: 1,
		Config: &dtv1.DeviceConfigUpdate_DeviceConfig{DeviceConfig: &dtv1.DeviceConfigPayload{
			DeviceId: "device-1", ConnectorId: "conn-1", Datapoints: datapoints,
		}},
	}
	if resp := cfgManager.Apply(update); !resp.GetSuccess() {
		t.Fatalf("Apply response = %+v, want success", resp)
	}
	if _, ok := manager.ResolveDevice("device-1"); !ok {
		t.Fatal("device-1 was not routed after config update")
	}
	update.UpdateId = "update-stale"
	update.EntityRevision = 1
	if resp := cfgManager.Apply(update); !resp.GetSuccess() || resp.GetErrorMessage() == "" {
		t.Fatalf("stale response = %+v, want success with message", resp)
	}
	bad := &dtv1.DeviceConfigUpdate{UpdateId: "bad-1", Action: dtv1.DeviceConfigUpdate_UPDATE_DEVICE, EntityRevision: 2}
	if resp := cfgManager.Apply(bad); resp.GetSuccess() {
		t.Fatalf("bad response = %+v, want failure", resp)
	}
	again := cfgManager.Apply(bad)
	if again.GetSuccess() || again.GetErrorMessage() == "" {
		t.Fatalf("duplicate bad response = %+v, want first failure", again)
	}
}

type cfgFakeConnector struct {
	cfg config.ConnectorConfig
}

func (c *cfgFakeConnector) Init(cfg config.ConnectorConfig) error { c.cfg = cfg; return nil }
func (c *cfgFakeConnector) Start(ctx context.Context, _ chan<- *dtv1.DeviceMessage) error { <-ctx.Done(); return nil }
func (c *cfgFakeConnector) SendCommand(context.Context, *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
	return &dtv1.CommandResponsePayload{Status: dtv1.CommandStatus_SUCCESS}, nil
}
func (c *cfgFakeConnector) Stop() error { return nil }
func (c *cfgFakeConnector) Status() connector.Status {
	return connector.Status{ConnectorID: c.cfg.ConnectorID, Protocol: c.cfg.Protocol, State: connector.StateRunning, DeviceCount: len(c.cfg.Devices)}
}
func (c *cfgFakeConnector) Devices() []dtv1.DeviceInfo {
	out := make([]dtv1.DeviceInfo, 0, len(c.cfg.Devices))
	for _, device := range c.cfg.Devices {
		out = append(out, connector.DeviceInfoFromConfig(c.cfg.ConnectorID, c.cfg.Protocol, nil, device, dtv1.DeviceState_ONLINE, time.Now()))
	}
	return out
}
func (c *cfgFakeConnector) ReloadConfig(cfg config.ConnectorConfig) error { c.cfg = cfg; return nil }

type cfgPublisher struct{}

func (cfgPublisher) Publish(*dtv1.DeviceMessage) error { return nil }

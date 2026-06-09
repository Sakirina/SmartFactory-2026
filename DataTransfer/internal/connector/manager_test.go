package connector

import (
	"context"
	"sync"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
)

func TestManagerDynamicConnectorAndDeviceReload(t *testing.T) {
	const protocol = "fake_reload"
	Register(protocol, func() Connector { return &fakeReloadConnector{} })
	publisher := &fakePublisher{}
	manager, err := NewManager(nil, publisher, nil)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = manager.Start(ctx)
	}()
	cfg := config.ConnectorConfig{
		ConnectorID: "conn-1",
		Protocol:    protocol,
		Devices: []config.DeviceConfig{
			{DeviceID: "device-1", DeviceName: "Device 1"},
		},
	}
	if err := manager.ApplyConnector(cfg); err != nil {
		t.Fatalf("ApplyConnector returned error: %v", err)
	}
	if _, ok := manager.ResolveDevice("device-1"); !ok {
		t.Fatal("device-1 route was not registered")
	}
	if err := manager.ApplyDevice("conn-1", config.DeviceConfig{DeviceID: "device-2", DeviceName: "Device 2"}); err != nil {
		t.Fatalf("ApplyDevice returned error: %v", err)
	}
	if _, ok := manager.ResolveDevice("device-2"); !ok {
		t.Fatal("device-2 route was not registered")
	}
	if err := manager.RemoveDevice("conn-1", "device-1"); err != nil {
		t.Fatalf("RemoveDevice returned error: %v", err)
	}
	if _, ok := manager.ResolveDevice("device-1"); ok {
		t.Fatal("device-1 route is still registered")
	}
	if !publisher.hasStatusFor("device-1") {
		t.Fatal("device-1 offline status was not published")
	}
	if err := manager.RemoveConnector("conn-1"); err != nil {
		t.Fatalf("RemoveConnector returned error: %v", err)
	}
	if _, ok := manager.ResolveDevice("device-2"); ok {
		t.Fatal("device-2 route is still registered")
	}
}

type fakeReloadConnector struct {
	mu      sync.Mutex
	cfg     config.ConnectorConfig
	started bool
	stopped bool
}

func (f *fakeReloadConnector) Init(cfg config.ConnectorConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
	return nil
}

func (f *fakeReloadConnector) Start(ctx context.Context, _ chan<- *dtv1.DeviceMessage) error {
	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
	<-ctx.Done()
	return nil
}

func (f *fakeReloadConnector) SendCommand(context.Context, *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
	return &dtv1.CommandResponsePayload{Status: dtv1.CommandStatus_SUCCESS}, nil
}

func (f *fakeReloadConnector) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return nil
}

func (f *fakeReloadConnector) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Status{ConnectorID: f.cfg.ConnectorID, Protocol: f.cfg.Protocol, State: StateRunning, DeviceCount: len(f.cfg.Devices)}
}

func (f *fakeReloadConnector) Devices() []dtv1.DeviceInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]dtv1.DeviceInfo, 0, len(f.cfg.Devices))
	for _, device := range f.cfg.Devices {
		out = append(out, DeviceInfoFromConfig(f.cfg.ConnectorID, f.cfg.Protocol, nil, device, dtv1.DeviceState_ONLINE, time.Now()))
	}
	return out
}

func (f *fakeReloadConnector) ReloadConfig(cfg config.ConnectorConfig) error {
	return f.Init(cfg)
}

type fakePublisher struct {
	mu       sync.Mutex
	messages []*dtv1.DeviceMessage
}

func (f *fakePublisher) Publish(msg *dtv1.DeviceMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, msg)
	return nil
}

func (f *fakePublisher) hasStatusFor(deviceID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, msg := range f.messages {
		if msg.GetType() == dtv1.MessageType_STATUS && msg.GetDevice().GetDeviceId() == deviceID {
			return true
		}
	}
	return false
}

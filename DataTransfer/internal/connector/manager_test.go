package connector

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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

// TestFailedReloadDoesNotCorruptStoredConfig 回归:ApplyDevice/RemoveDevice 必须
// 先克隆配置再修改;否则失败回滚后 Manager 中保存的配置已被就地污染。
func TestFailedReloadDoesNotCorruptStoredConfig(t *testing.T) {
	const protocol = "fake_failing_reload"
	conn := &failingReloadConnector{}
	Register(protocol, func() Connector { return conn })
	manager, err := NewManager([]config.ConnectorConfig{{
		ConnectorID: "conn-f",
		Protocol:    protocol,
		Devices: []config.DeviceConfig{
			{DeviceID: "dev-a", DeviceName: "A"},
			{DeviceID: "dev-b", DeviceName: "B"},
		},
	}}, &fakePublisher{}, nil)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	conn.failReload.Store(true)
	if err := manager.RemoveDevice("conn-f", "dev-a"); err == nil {
		t.Fatal("RemoveDevice should fail when reload fails")
	}
	cfg, ok := manager.ConnectorConfig("conn-f")
	if !ok || len(cfg.Devices) != 2 {
		t.Fatalf("stored config corrupted after failed reload: devices = %+v", cfg.Devices)
	}
	if cfg.Devices[0].DeviceID != "dev-a" || cfg.Devices[1].DeviceID != "dev-b" {
		t.Fatalf("stored device order corrupted: %+v", cfg.Devices)
	}

	if err := manager.ApplyDevice("conn-f", config.DeviceConfig{DeviceID: "dev-c"}); err == nil {
		t.Fatal("ApplyDevice should fail when reload fails")
	}
	cfg, _ = manager.ConnectorConfig("conn-f")
	if len(cfg.Devices) != 2 {
		t.Fatalf("stored config gained device after failed apply: %+v", cfg.Devices)
	}

	conn.failReload.Store(false)
	if err := manager.RemoveDevice("conn-f", "dev-a"); err != nil {
		t.Fatalf("RemoveDevice after recovery returned error: %v", err)
	}
	cfg, _ = manager.ConnectorConfig("conn-f")
	if len(cfg.Devices) != 1 || cfg.Devices[0].DeviceID != "dev-b" {
		t.Fatalf("devices after successful remove = %+v", cfg.Devices)
	}
}

type failingReloadConnector struct {
	fakeReloadConnector
	failReload atomic.Bool
}

func (f *failingReloadConnector) ReloadConfig(cfg config.ConnectorConfig) error {
	if f.failReload.Load() {
		return errReloadRejected
	}
	return f.fakeReloadConnector.ReloadConfig(cfg)
}

var errReloadRejected = errors.New("reload rejected by connector")

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

func (f *fakeReloadConnector) Devices() []*dtv1.DeviceInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*dtv1.DeviceInfo, 0, len(f.cfg.Devices))
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

package sidecar

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/pkg/plugin"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

type fixturePlugin struct{ cfg config.ConnectorConfig }

func (f *fixturePlugin) Init(raw json.RawMessage) error { return json.Unmarshal(raw, &f.cfg) }
func (f *fixturePlugin) Run(ctx context.Context, emit func(*dt.DeviceMessage) error) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if e := emit(&dt.DeviceMessage{MessageId: "fixture", Device: &dt.DeviceIdentity{DeviceId: f.cfg.Devices[0].DeviceID, ConnectorId: f.cfg.ConnectorID}, Type: dt.MessageType_TELEMETRY}); e != nil {
			return e
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (f *fixturePlugin) Command(_ context.Context, msg *dt.DeviceMessage) (*dt.CommandResponsePayload, error) {
	if msg.GetControl().GetAction() == "crash" {
		os.Exit(23)
	}
	return &dt.CommandResponsePayload{CommandId: msg.CommandId, Status: dt.CommandStatus_SUCCESS}, nil
}
func TestPluginProcessHelper(t *testing.T) {
	if os.Getenv("SF_PLUGIN_SOCKET") == "" {
		return
	}
	if e := plugin.Serve(context.Background(), &fixturePlugin{}); e != nil {
		os.Exit(24)
	}
	os.Exit(0)
}
func TestProcessCrashDoesNotStopAnotherConnector(t *testing.T) {
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	makeProcess := func(id string) (*Process, <-chan *dt.DeviceMessage, <-chan error) {
		p := &Process{}
		e := p.Init(config.ConnectorConfig{ConnectorID: id, Protocol: "process", Process: &config.ProcessConfig{Executable: executable, Args: []string{"-test.run=^TestPluginProcessHelper$"}}, Devices: []config.DeviceConfig{{DeviceID: id}}})
		if e != nil {
			t.Fatal(e)
		}
		ch := make(chan *dt.DeviceMessage, 64)
		done := make(chan error, 1)
		go func() { done <- p.Start(ctx, ch) }()
		select {
		case <-ch:
		case e := <-done:
			t.Fatalf("plugin failed: %v", e)
		case <-ctx.Done():
			t.Fatal("plugin startup timeout")
		}
		return p, ch, done
	}
	first, _, failed := makeProcess("crashing")
	other, updates, otherDone := makeProcess("continuing")
	defer other.Stop()
	response, e := first.SendCommand(ctx, &dt.DeviceMessage{CommandId: "crash", Device: &dt.DeviceIdentity{DeviceId: "crashing"}, Type: dt.MessageType_CONTROL, Payload: &dt.DeviceMessage_Control{Control: &dt.ControlPayload{Action: "crash"}}})
	if e == nil {
		t.Fatalf("crash reported a business result %v", response)
	}
	select {
	case e := <-failed:
		if e == nil {
			t.Fatal("plugin crash not reported")
		}
	case <-ctx.Done():
		t.Fatal("crashed process did not return")
	}
	for i := 0; i < 5; i++ {
		select {
		case <-updates:
		case <-otherDone:
			t.Fatal("other connector stopped")
		case <-ctx.Done():
			t.Fatal("other connector stalled")
		}
	}
	if other.Status().State != "running" {
		t.Fatal("healthy plugin is not running")
	}
	cancel()
	select {
	case <-otherDone:
	case <-time.After(time.Second):
		t.Fatal("healthy plugin did not stop")
	}
}

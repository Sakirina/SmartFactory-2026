package sidecar

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/simulator"
)

func TestHTTPExampleProcessCollectsAndControlsSimulator(t *testing.T) {
	executable := os.Getenv("SF_HTTP_PLUGIN_BINARY")
	if executable == "" {
		t.Skip("set SF_HTTP_PLUGIN_BINARY to the built examples/http-plugin")
	}
	state, e := simulator.OpenState(filepath.Join(t.TempDir(), "simulator.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer state.DB.Close()
	sim := &simulator.Simulator{State: state}
	web := httptest.NewServer(sim.Handler())
	defer web.Close()
	p := &Process{}
	if e = p.Init(config.ConnectorConfig{ConnectorID: "http-process", Protocol: "process", Connection: config.ConnectionConfig{URL: web.URL}, Process: &config.ProcessConfig{Executable: executable}, Devices: []config.DeviceConfig{{DeviceID: "climate-1"}}}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	messages := make(chan *dt.DeviceMessage, 32)
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx, messages) }()
	select {
	case m := <-messages:
		if len(m.GetTelemetry().GetDatapoints()) == 0 {
			t.Fatal("plugin did not collect simulator telemetry")
		}
	case e := <-done:
		t.Fatal(e)
	case <-ctx.Done():
		t.Fatal("plugin startup timed out")
	}
	response, e := p.SendCommand(ctx, &dt.DeviceMessage{CommandId: "fan-command", Device: &dt.DeviceIdentity{DeviceId: "climate-1"}, Type: dt.MessageType_CONTROL, Payload: &dt.DeviceMessage_Control{Control: &dt.ControlPayload{Action: "set_fan", Params: map[string]string{"value": "true"}}}})
	if e != nil || response.Status != dt.CommandStatus_SUCCESS {
		t.Fatal(e, response)
	}
	if state.Snapshot()["climate-1"]["fan"] != true {
		t.Fatal("plugin control did not change the physical simulator")
	}
	state.Tick()
	if state.Snapshot()["climate-1"]["temperature"].(float64) >= 26 {
		t.Fatal("actuator did not alter subsequent sensor readings")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("plugin process did not stop")
	}
}

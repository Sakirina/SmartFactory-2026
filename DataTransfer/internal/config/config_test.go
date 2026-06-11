package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppliesEnvironmentOverrides(t *testing.T) {
	t.Setenv("DT_RUN_MODE", RunModeSplit)
	t.Setenv("DT_MQTT_BROKER", "tcp://127.0.0.1:1883")
	t.Setenv("DT_MQTT_GATEWAY_ID", "edge-001")
	t.Setenv("DT_GRPC_ENABLED", "false")
	t.Setenv("DT_RUNTIME_RING_SIZE", "42")
	t.Setenv("DT_BUFFER_PATH", filepath.Join(t.TempDir(), "buffer.db"))
	t.Setenv("DT_BUFFER_RESUME_BATCH_SIZE", "7")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.RunMode != RunModeSplit {
		t.Fatalf("RunMode = %q, want %q", cfg.RunMode, RunModeSplit)
	}
	if !cfg.MQTT.Enabled {
		t.Fatal("MQTT should be enabled for split mode")
	}
	if cfg.MQTT.ClientID != "gateway-edge-001" {
		t.Fatalf("ClientID = %q", cfg.MQTT.ClientID)
	}
	if cfg.Runtime.RingSize != 42 {
		t.Fatalf("RingSize = %d", cfg.Runtime.RingSize)
	}
	if !cfg.Buffer.Enabled {
		t.Fatal("Buffer should be enabled for split mode")
	}
	if cfg.Buffer.ResumeBatchSize != 7 {
		t.Fatalf("ResumeBatchSize = %d, want 7", cfg.Buffer.ResumeBatchSize)
	}
}

func TestLoadRejectsSplitWithoutBroker(t *testing.T) {
	t.Setenv("DT_RUN_MODE", RunModeSplit)
	t.Setenv("DT_MQTT_GATEWAY_ID", "edge-001")

	_, err := Load("")
	if err == nil {
		t.Fatal("Load succeeded, want validation error")
	}
}

func TestLoadRejectsUnsupportedBufferStorage(t *testing.T) {
	t.Setenv("DT_RUN_MODE", RunModeSplit)
	t.Setenv("DT_MQTT_BROKER", "tcp://127.0.0.1:1883")
	t.Setenv("DT_MQTT_GATEWAY_ID", "edge-001")
	t.Setenv("DT_BUFFER_STORAGE_TYPE", "file")

	_, err := Load("")
	if err == nil || !strings.Contains(err.Error(), "buffer.storage_type") {
		t.Fatalf("Load error = %v, want buffer.storage_type validation error", err)
	}
}

func TestLoadConnectorStaticConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "datatransfer.yaml")
	data := []byte(`
run_mode: embedded
management:
  addr: "127.0.0.1:0"
grpc:
  enabled: true
  addr: "127.0.0.1:0"
mqtt:
  enabled: false
runtime:
  ring_size: 16
  command_ttl_seconds: 60
connectors:
  - connector_id: "modbus-1"
    protocol: "MODBUS_TCP"
    connection:
      host: "127.0.0.1"
      port: 1502
    devices:
      - device_id: "device-1"
        datapoints:
          - key: "temperature"
            register_type: "HOLDING_REGISTER"
            address: 10
            data_type: "INT16"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	conn := cfg.Connectors[0]
	if conn.Protocol != "modbus_tcp" {
		t.Fatalf("protocol = %q, want modbus_tcp", conn.Protocol)
	}
	if conn.Connection.TimeoutMillis != 1000 {
		t.Fatalf("connection timeout = %d, want 1000", conn.Connection.TimeoutMillis)
	}
	if conn.Polling.IntervalMillis != 1000 || conn.Polling.TimeoutMillis != 1000 {
		t.Fatalf("polling = %+v, want default 1000ms interval and timeout", conn.Polling)
	}
	dp := conn.Devices[0].Datapoints[0]
	if dp.RegisterType != "holding_register" || dp.DataType != "int16" {
		t.Fatalf("datapoint normalization = %+v", dp)
	}
}

func TestLoadRejectsDuplicateDeviceID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "datatransfer.yaml")
	data := []byte(`
management:
  addr: "127.0.0.1:0"
grpc:
  enabled: true
  addr: "127.0.0.1:0"
connectors:
  - connector_id: "modbus-1"
    protocol: "modbus_tcp"
    devices:
      - device_id: "device-1"
        datapoints:
          - key: "a"
            register_type: "coil"
      - device_id: "device-1"
        datapoints:
          - key: "b"
            register_type: "coil"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate device_id") {
		t.Fatalf("Load error = %v, want duplicate device_id", err)
	}
}

func TestEnvironmentAndReflectionValidation(t *testing.T) {
	base := Defaults()
	if base.Environment != EnvProduction {
		t.Fatalf("default environment = %q, want production (safe default)", base.Environment)
	}
	if base.GRPC.Reflection {
		t.Fatal("reflection must default to disabled")
	}

	cfg := Defaults()
	cfg.GRPC.Reflection = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("production + reflection must be rejected")
	}

	cfg = Defaults()
	cfg.Environment = "development"
	cfg.GRPC.Reflection = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("development + reflection should be allowed: %v", err)
	}

	cfg = Defaults()
	cfg.Environment = "staging"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown environment must be rejected")
	}

	cfg = Defaults()
	cfg.GRPC.TLS.Enabled = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("grpc tls without cert/key must be rejected")
	}
}

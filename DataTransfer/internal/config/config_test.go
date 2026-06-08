package config

import "testing"

func TestLoadAppliesEnvironmentOverrides(t *testing.T) {
	t.Setenv("DT_RUN_MODE", RunModeSplit)
	t.Setenv("DT_MQTT_BROKER", "tcp://127.0.0.1:1883")
	t.Setenv("DT_MQTT_GATEWAY_ID", "edge-001")
	t.Setenv("DT_GRPC_ENABLED", "false")
	t.Setenv("DT_RUNTIME_RING_SIZE", "42")

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
}

func TestLoadRejectsSplitWithoutBroker(t *testing.T) {
	t.Setenv("DT_RUN_MODE", RunModeSplit)
	t.Setenv("DT_MQTT_GATEWAY_ID", "edge-001")

	_, err := Load("")
	if err == nil {
		t.Fatal("Load succeeded, want validation error")
	}
}

package modbus

import (
	"math"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
)

func TestConverterBuildsTelemetryWithScaleAndMergedTags(t *testing.T) {
	scale := 0.1
	connectorCfg := config.ConnectorConfig{
		ConnectorID: "modbus-1",
		Protocol:    Protocol,
		DefaultTags: map[string]string{
			"site":     "lab",
			"workshop": "default",
		},
	}
	device := config.DeviceConfig{
		DeviceID:   "device-1",
		DeviceName: "Demo PLC",
		DeviceType: "plc",
		Tags:       map[string]string{"workshop": "A"},
	}
	readings := []Reading{
		{
			Datapoint: config.DatapointConfig{
				Key:          "temperature",
				RegisterType: RegisterTypeHoldingRegister,
				DataType:     DataTypeInt16,
				Scale:        &scale,
				Unit:         "celsius",
				Quality:      "good",
			},
			Raw:       []uint16{235},
			Timestamp: time.Now().UnixMilli(),
		},
		{
			Datapoint: config.DatapointConfig{
				Key:          "running",
				RegisterType: RegisterTypeCoil,
				DataType:     DataTypeBool,
			},
			Raw:       true,
			Timestamp: time.Now().UnixMilli(),
		},
	}

	msg, skipped, err := NewConverter(connectorCfg).BuildTelemetry(device, readings)
	if err != nil {
		t.Fatalf("BuildTelemetry returned error: %v", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none", skipped)
	}
	if msg.GetDevice().GetConnectorId() != "modbus-1" {
		t.Fatalf("connector id = %q", msg.GetDevice().GetConnectorId())
	}
	if msg.GetDevice().GetTags()["site"] != "lab" || msg.GetDevice().GetTags()["workshop"] != "A" {
		t.Fatalf("tags = %+v", msg.GetDevice().GetTags())
	}

	datapoints := msg.GetTelemetry().GetDatapoints()
	if len(datapoints) != 2 {
		t.Fatalf("datapoints = %d, want 2", len(datapoints))
	}
	if math.Abs(datapoints[0].GetValue().GetDoubleValue()-23.5) > 0.0001 {
		t.Fatalf("temperature = %v, want 23.5", datapoints[0].GetValue())
	}
	if datapoints[0].GetQuality() != dtv1.DataQuality_GOOD {
		t.Fatalf("quality = %s, want GOOD", datapoints[0].GetQuality())
	}
	if !datapoints[1].GetValue().GetBoolValue() {
		t.Fatal("running datapoint should be true")
	}
}

func TestEncodeAndDecodeFloat32RegisterPair(t *testing.T) {
	registers, err := encodeRegisterValues("12.5", DataTypeFloat32)
	if err != nil {
		t.Fatalf("encodeRegisterValues returned error: %v", err)
	}
	value, err := decodeRegisters(registers, config.DatapointConfig{DataType: DataTypeFloat32})
	if err != nil {
		t.Fatalf("decodeRegisters returned error: %v", err)
	}
	if math.Abs(value.GetDoubleValue()-12.5) > 0.0001 {
		t.Fatalf("float32 value = %v, want 12.5", value.GetDoubleValue())
	}
}

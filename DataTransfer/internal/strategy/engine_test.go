package strategy

import (
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
)

type fakeResolver struct {
	datapoint *config.ReportStrategyConfig
	connector *config.ReportStrategyConfig
}

func (f fakeResolver) StrategyFor(string, string, string) (*config.ReportStrategyConfig, *config.ReportStrategyConfig) {
	return f.datapoint, f.connector
}

func telemetry(deviceID string, key string, value float64) *dtv1.DeviceMessage {
	return &dtv1.DeviceMessage{
		MessageId: "msg-" + deviceID,
		Direction: dtv1.Direction_UPSTREAM,
		Type:      dtv1.MessageType_TELEMETRY,
		Device:    &dtv1.DeviceIdentity{DeviceId: deviceID, ConnectorId: "conn-1"},
		Payload: &dtv1.DeviceMessage_Telemetry{Telemetry: &dtv1.TelemetryPayload{Datapoints: []*dtv1.Datapoint{
			{Key: key, Value: &dtv1.DataValue{Kind: &dtv1.DataValue_DoubleValue{DoubleValue: value}}},
		}}},
	}
}

func TestOnReceivedPassesEverything(t *testing.T) {
	engine := NewEngine(config.ReportStrategyConfig{Mode: ModeOnReceived})
	for i := 0; i < 3; i++ {
		if engine.Apply(telemetry("d1", "temp", 1.0)) == nil {
			t.Fatal("ON_RECEIVED must not filter")
		}
	}
}

func TestOnChangeFiltersUnchangedAndHonorsDeadband(t *testing.T) {
	engine := NewEngine(config.ReportStrategyConfig{Mode: ModeOnChange, Deadband: 0.5})
	if engine.Apply(telemetry("d1", "temp", 20.0)) == nil {
		t.Fatal("first value must pass")
	}
	if engine.Apply(telemetry("d1", "temp", 20.3)) != nil {
		t.Fatal("change within deadband must be filtered")
	}
	if engine.Apply(telemetry("d1", "temp", 21.0)) == nil {
		t.Fatal("change beyond deadband must pass")
	}
	filtered, delivered, points := engine.Stats()
	if filtered != 1 || delivered != 2 || points != 1 {
		t.Fatalf("stats = (%d, %d, %d), want (1, 2, 1)", filtered, delivered, points)
	}
}

func TestOnReportPeriodThrottles(t *testing.T) {
	engine := NewEngine(config.ReportStrategyConfig{Mode: ModeOnReportPeriod, PeriodSeconds: 3600})
	if engine.Apply(telemetry("d1", "temp", 1)) == nil {
		t.Fatal("first sample must pass (period elapsed from zero)")
	}
	if engine.Apply(telemetry("d1", "temp", 2)) != nil {
		t.Fatal("second sample within period must be filtered even if value changed")
	}
}

func TestOnChangeOrPeriodPassesOnChange(t *testing.T) {
	engine := NewEngine(config.ReportStrategyConfig{Mode: ModeOnChangeOrPeriod, PeriodSeconds: 3600})
	if engine.Apply(telemetry("d1", "temp", 1)) == nil {
		t.Fatal("first sample must pass")
	}
	if engine.Apply(telemetry("d1", "temp", 1)) != nil {
		t.Fatal("unchanged within period must be filtered")
	}
	if engine.Apply(telemetry("d1", "temp", 2)) == nil {
		t.Fatal("changed value must pass immediately")
	}
}

func TestPriorityDatapointOverConnectorOverGlobal(t *testing.T) {
	engine := NewEngine(config.ReportStrategyConfig{Mode: ModeOnChange})
	engine.SetResolver(fakeResolver{
		datapoint: &config.ReportStrategyConfig{Mode: ModeOnReceived},
		connector: &config.ReportStrategyConfig{Mode: ModeOnChange},
	})
	// 数据点级 ON_RECEIVED 覆盖 Connector/全局 ON_CHANGE:重复值也放行。
	if engine.Apply(telemetry("d1", "temp", 5)) == nil || engine.Apply(telemetry("d1", "temp", 5)) == nil {
		t.Fatal("datapoint-level ON_RECEIVED must override ON_CHANGE")
	}
}

func TestNonTelemetryNeverFiltered(t *testing.T) {
	engine := NewEngine(config.ReportStrategyConfig{Mode: ModeOnChange})
	status := &dtv1.DeviceMessage{
		Type:      dtv1.MessageType_STATUS,
		Direction: dtv1.Direction_UPSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "d1"},
	}
	for i := 0; i < 2; i++ {
		if engine.Apply(status) == nil {
			t.Fatal("STATUS must never be filtered (FR-S-036)")
		}
	}
}

func TestPartialFilterRebuildsMessage(t *testing.T) {
	engine := NewEngine(config.ReportStrategyConfig{Mode: ModeOnChange})
	msg := &dtv1.DeviceMessage{
		Direction: dtv1.Direction_UPSTREAM,
		Type:      dtv1.MessageType_TELEMETRY,
		Device:    &dtv1.DeviceIdentity{DeviceId: "d1", ConnectorId: "conn-1"},
		Payload: &dtv1.DeviceMessage_Telemetry{Telemetry: &dtv1.TelemetryPayload{Datapoints: []*dtv1.Datapoint{
			{Key: "a", Value: &dtv1.DataValue{Kind: &dtv1.DataValue_DoubleValue{DoubleValue: 1}}},
			{Key: "b", Value: &dtv1.DataValue{Kind: &dtv1.DataValue_DoubleValue{DoubleValue: 1}}},
		}}},
	}
	if out := engine.Apply(msg); len(out.GetTelemetry().GetDatapoints()) != 2 {
		t.Fatal("first sample must pass both datapoints")
	}
	second := &dtv1.DeviceMessage{
		Direction: dtv1.Direction_UPSTREAM,
		Type:      dtv1.MessageType_TELEMETRY,
		Device:    &dtv1.DeviceIdentity{DeviceId: "d1", ConnectorId: "conn-1"},
		Payload: &dtv1.DeviceMessage_Telemetry{Telemetry: &dtv1.TelemetryPayload{Datapoints: []*dtv1.Datapoint{
			{Key: "a", Value: &dtv1.DataValue{Kind: &dtv1.DataValue_DoubleValue{DoubleValue: 1}}},
			{Key: "b", Value: &dtv1.DataValue{Kind: &dtv1.DataValue_DoubleValue{DoubleValue: 2}}},
		}}},
	}
	out := engine.Apply(second)
	if out == nil {
		t.Fatal("message with one changed datapoint must pass")
	}
	points := out.GetTelemetry().GetDatapoints()
	if len(points) != 1 || points[0].GetKey() != "b" {
		t.Fatalf("datapoints = %+v, want only changed key b", points)
	}
	// 原消息不得被就地修改(其他订阅路径可能仍持有引用)。
	if len(second.GetTelemetry().GetDatapoints()) != 2 {
		t.Fatal("input message must not be mutated")
	}
}

func TestStrategyChangeRebuildsState(t *testing.T) {
	engine := NewEngine(config.ReportStrategyConfig{Mode: ModeOnChange})
	if engine.Apply(telemetry("d1", "temp", 1)) == nil {
		t.Fatal("first sample must pass")
	}
	if engine.Apply(telemetry("d1", "temp", 1)) != nil {
		t.Fatal("unchanged must be filtered")
	}
	// 全局策略热更新(模拟 UPDATE_GLOBAL):指纹变化,旧状态失效,首条重新放行。
	engine.SetGlobal(config.ReportStrategyConfig{Mode: ModeOnChange, Deadband: 0.25})
	if engine.Apply(telemetry("d1", "temp", 1)) == nil {
		t.Fatal("after strategy change the state must rebuild and first sample pass")
	}
}

func TestPeriodElapsedAllowsNextSample(t *testing.T) {
	engine := NewEngine(config.ReportStrategyConfig{Mode: ModeOnReportPeriod, PeriodSeconds: 1})
	if engine.Apply(telemetry("d1", "temp", 1)) == nil {
		t.Fatal("first sample must pass")
	}
	if engine.Apply(telemetry("d1", "temp", 2)) != nil {
		t.Fatal("inside period must filter")
	}
	time.Sleep(1100 * time.Millisecond)
	if engine.Apply(telemetry("d1", "temp", 3)) == nil {
		t.Fatal("after period elapsed the latest sample must pass")
	}
}

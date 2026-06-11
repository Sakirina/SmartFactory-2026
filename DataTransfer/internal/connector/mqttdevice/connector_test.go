package mqttdevice

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	paho "github.com/eclipse/paho.mqtt.golang"
	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/tidwall/gjson"
)

func TestConnectorTelemetryAndCommandThroughBroker(t *testing.T) {
	broker, cleanup := startBroker(t)
	defer cleanup()

	conn := NewConnector()
	cfg := config.ConnectorConfig{
		ConnectorID: "mqtt-device-1",
		Protocol:    Protocol,
		Connection: config.ConnectionConfig{
			URL:          broker,
			CommandTopic: "devices/{device_id}/command",
		},
		Devices: []config.DeviceConfig{{
			DeviceID: "device-1",
			Datapoints: []config.DatapointConfig{
				{Key: "temperature", Source: "temp", DataType: "float64", Unit: "C"},
				{Key: "running", Source: "running", DataType: "bool"},
			},
		}},
	}
	if err := conn.Init(cfg); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	upstream := make(chan *dtv1.DeviceMessage, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = conn.Start(ctx, upstream)
	}()
	waitForConnectorRunning(t, conn)

	publisher := mqttClient(t, broker, "device-publisher")
	defer publisher.Disconnect(100)
	token := publisher.Publish("devices/device-1/telemetry", 1, false, []byte(`{"temp":21.5,"running":true}`))
	if !token.WaitTimeout(time.Second) || token.Error() != nil {
		t.Fatalf("telemetry publish failed: %v", token.Error())
	}
	msg := receiveMessage(t, upstream)
	if msg.GetType() != dtv1.MessageType_TELEMETRY || len(msg.GetTelemetry().GetDatapoints()) != 2 {
		t.Fatalf("message = %+v, want telemetry with two datapoints", msg)
	}

	commandPayloads := subscribeRaw(t, broker, "devices/device-1/command")
	resp, err := conn.SendCommand(context.Background(), &dtv1.DeviceMessage{
		CommandId: "cmd-1",
		Direction: dtv1.Direction_DOWNSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "device-1"},
		Type:      dtv1.MessageType_CONTROL,
		Payload:   &dtv1.DeviceMessage_Control{Control: &dtv1.ControlPayload{Action: "start", Params: map[string]string{"speed": "10"}}},
	})
	if err != nil || resp.GetStatus() != dtv1.CommandStatus_SUCCESS {
		t.Fatalf("SendCommand = (%v, %v), want success", resp, err)
	}
	payload := receivePayload(t, commandPayloads)
	if got := gjson.GetBytes(payload, "command_id").String(); got != "cmd-1" {
		t.Fatalf("command_id = %q, want cmd-1", got)
	}
	if got := gjson.GetBytes(payload, "action").String(); got != "start" {
		t.Fatalf("action = %q, want start", got)
	}
}

func startBroker(t *testing.T) (string, func()) {
	t.Helper()
	server := mqttserver.New(&mqttserver.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatalf("AddHook returned error: %v", err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "test", Address: "127.0.0.1:0"})
	if err := server.AddListener(tcp); err != nil {
		t.Fatalf("AddListener returned error: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	return "tcp://" + tcp.Address(), func() {
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
}

func mqttClient(t *testing.T, broker string, id string) paho.Client {
	t.Helper()
	client := paho.NewClient(paho.NewClientOptions().AddBroker(broker).SetClientID(id + "-" + time.Now().Format("150405.000000")).SetConnectTimeout(time.Second))
	token := client.Connect()
	if !token.WaitTimeout(time.Second) || token.Error() != nil {
		t.Fatalf("mqtt connect failed: %v", token.Error())
	}
	return client
}

func subscribeRaw(t *testing.T, broker string, topic string) <-chan []byte {
	t.Helper()
	ch := make(chan []byte, 1)
	client := mqttClient(t, broker, "command-subscriber")
	t.Cleanup(func() {
		if client.IsConnected() {
			client.Disconnect(100)
		}
	})
	token := client.Subscribe(topic, 1, func(_ paho.Client, msg paho.Message) {
		ch <- append([]byte(nil), msg.Payload()...)
	})
	if !token.WaitTimeout(time.Second) || token.Error() != nil {
		t.Fatalf("mqtt subscribe failed: %v", token.Error())
	}
	return ch
}

func receivePayload(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case payload := <-ch:
		return payload
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for payload")
		return nil
	}
}

func receiveMessage(t *testing.T, ch <-chan *dtv1.DeviceMessage) *dtv1.DeviceMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
		return nil
	}
}

func waitForConnectorRunning(t *testing.T, conn *Connector) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn.Status().State == "running" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("connector did not reach running state")
}

func TestMatchRouteSupportsCustomTopicTemplates(t *testing.T) {
	routes := topicRoutes(config.ConnectorConfig{
		Connection: config.ConnectionConfig{
			TelemetryTopic: "factory/{device_id}/data",
			StatusTopic:    "factory/{device_id}/state",
		},
	})
	kind, deviceID := matchRoute(routes, "factory/sensor-9/data")
	if kind != kindTelemetry || deviceID != "sensor-9" {
		t.Fatalf("custom telemetry route = (%q, %q), want (telemetry, sensor-9)", kind, deviceID)
	}
	kind, deviceID = matchRoute(routes, "factory/sensor-9/state")
	if kind != kindStatus || deviceID != "sensor-9" {
		t.Fatalf("custom status route = (%q, %q), want (status, sensor-9)", kind, deviceID)
	}
	// 默认模板仍然生效(event 未自定义)。
	kind, deviceID = matchRoute(routes, "devices/sensor-1/event")
	if kind != kindEvent || deviceID != "sensor-1" {
		t.Fatalf("default event route = (%q, %q), want (event, sensor-1)", kind, deviceID)
	}
	if kind, _ := matchRoute(routes, "factory/sensor-9/unknown"); kind != "" {
		t.Fatalf("unknown topic matched kind %q", kind)
	}
	if kind, _ := matchRoute(routes, "factory/a/b/data"); kind != "" {
		t.Fatalf("segment count mismatch matched kind %q", kind)
	}
}

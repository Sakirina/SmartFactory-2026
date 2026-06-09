package mqtt

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/buffer"
	"competition2026/product/datatransfer/internal/config"
	dtruntime "competition2026/product/datatransfer/internal/runtime"
	paho "github.com/eclipse/paho.mqtt.golang"
	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"google.golang.org/protobuf/proto"
)

func TestReliableAdapterPublishesThroughEmbeddedBroker(t *testing.T) {
	broker, cleanup := startEmbeddedBroker(t)
	defer cleanup()

	cfg := testMQTTConfig(broker)
	store := openMQTTTestStore(t)
	defer store.Close()
	rt := dtruntime.New(splitRuntimeConfig(cfg))
	adapter := New(cfg, rt, testLogger(), WithBuffer(store, testBufferConfig(storePath(t))))
	rt.AttachUpstreamSink(adapter)
	rt.AttachPersistentBuffer(adapter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = adapter.Start(ctx)
	}()
	waitForAdapterConnected(t, adapter)

	received := subscribeBatch(t, broker, upstreamTopic(t, cfg.GatewayID, dtv1.MessageType_TELEMETRY))
	if err := rt.Publish(mqttTestMessage("msg-live")); err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	batch := receiveBatch(t, received)
	if got := batch.GetMessages()[0].GetMessageId(); got != "msg-live" {
		t.Fatalf("message id = %q, want msg-live", got)
	}
	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats returned error: %v", err)
	}
	if stats.Completed != 1 {
		t.Fatalf("completed = %d, want 1", stats.Completed)
	}
}

func TestReplayWorkerPublishesPendingMessages(t *testing.T) {
	broker, cleanup := startEmbeddedBroker(t)
	defer cleanup()

	cfg := testMQTTConfig(broker)
	store := openMQTTTestStore(t)
	defer store.Close()
	if _, err := store.Enqueue(context.Background(), mqttTestMessage("msg-pending")); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	received := subscribeBatch(t, broker, upstreamTopic(t, cfg.GatewayID, dtv1.MessageType_TELEMETRY))

	rt := dtruntime.New(splitRuntimeConfig(cfg))
	bufferCfg := testBufferConfig(storePath(t))
	bufferCfg.ResumeBatchSize = 1
	bufferCfg.ResumeRateLimit = 1000
	adapter := New(cfg, rt, testLogger(), WithBuffer(store, bufferCfg))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = adapter.Start(ctx)
	}()
	waitForAdapterConnected(t, adapter)

	batch := receiveBatch(t, received)
	if got := batch.GetMessages()[0].GetMessageId(); got != "msg-pending" {
		t.Fatalf("message id = %q, want msg-pending", got)
	}
	waitForCondition(t, func() bool {
		stats, err := store.Stats(context.Background())
		return err == nil && stats.Completed == 1
	})
	if snapshot := adapter.BufferSnapshot(); snapshot.ReplayBatchTotal == 0 {
		t.Fatalf("ReplayBatchTotal = %d, want > 0", snapshot.ReplayBatchTotal)
	}
}

func TestExternalBrokerSmoke(t *testing.T) {
	broker := os.Getenv("DT_MQTT_SMOKE_BROKER")
	if broker == "" {
		t.Skip("DT_MQTT_SMOKE_BROKER is not set")
	}
	cfg := testMQTTConfig(broker)
	cfg.Username = os.Getenv("DT_MQTT_SMOKE_USERNAME")
	cfg.Password = os.Getenv("DT_MQTT_SMOKE_PASSWORD")
	cfg.TLS.Enabled = os.Getenv("DT_MQTT_SMOKE_TLS_ENABLED") == "true"
	cfg.TLS.InsecureSkipVerify = os.Getenv("DT_MQTT_SMOKE_TLS_INSECURE_SKIP_VERIFY") == "true"
	store := openMQTTTestStore(t)
	defer store.Close()
	rt := dtruntime.New(splitRuntimeConfig(cfg))
	adapter := New(cfg, rt, testLogger(), WithBuffer(store, testBufferConfig(storePath(t))))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = adapter.Start(ctx)
	}()
	waitForAdapterConnected(t, adapter)
}

func startEmbeddedBroker(t *testing.T) (string, func()) {
	t.Helper()
	server := mqttserver.New(&mqttserver.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatalf("AddHook returned error: %v", err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "test", Address: "127.0.0.1:0"})
	if err := server.AddListener(tcp); err != nil {
		t.Fatalf("AddListener returned error: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve()
	}()
	return "tcp://" + tcp.Address(), func() {
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
}

func subscribeBatch(t *testing.T, broker string, topic string) <-chan *dtv1.DeviceMessageBatch {
	t.Helper()
	ch := make(chan *dtv1.DeviceMessageBatch, 1)
	opts := paho.NewClientOptions().
		AddBroker(broker).
		SetClientID("subscriber-" + time.Now().Format("150405.000000")).
		SetConnectTimeout(time.Second)
	client := paho.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(time.Second) || token.Error() != nil {
		t.Fatalf("subscriber connect failed: %v", token.Error())
	}
	t.Cleanup(func() {
		if client.IsConnected() {
			client.Disconnect(100)
		}
	})
	token = client.Subscribe(topic, 2, func(_ paho.Client, msg paho.Message) {
		var batch dtv1.DeviceMessageBatch
		if err := proto.Unmarshal(msg.Payload(), &batch); err == nil {
			ch <- &batch
		}
	})
	if !token.WaitTimeout(time.Second) || token.Error() != nil {
		t.Fatalf("subscriber subscribe failed: %v", token.Error())
	}
	return ch
}

func receiveBatch(t *testing.T, ch <-chan *dtv1.DeviceMessageBatch) *dtv1.DeviceMessageBatch {
	t.Helper()
	select {
	case batch := <-ch:
		return batch
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for MQTT batch")
		return nil
	}
}

func upstreamTopic(t *testing.T, gatewayID string, messageType dtv1.MessageType) string {
	t.Helper()
	topic, _, err := (Topics{GatewayID: gatewayID}).Upstream(messageType)
	if err != nil {
		t.Fatalf("Upstream returned error: %v", err)
	}
	return topic
}

func waitForAdapterConnected(t *testing.T, adapter *Adapter) {
	t.Helper()
	waitForCondition(t, adapter.IsConnected)
}

func waitForCondition(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func openMQTTTestStore(t *testing.T) *buffer.Store {
	t.Helper()
	cfg := testBufferConfig(storePath(t))
	store, err := buffer.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buffer.Open returned error: %v", err)
	}
	return store
}

func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "buffer.db")
}

func testMQTTConfig(broker string) config.MQTTConfig {
	return config.MQTTConfig{
		Enabled:        true,
		Broker:         broker,
		GatewayID:      "gateway-test",
		ClientID:       "gateway-test-client-" + time.Now().Format("150405.000000"),
		ConnectTimeout: 1,
	}
}

func splitRuntimeConfig(mqttCfg config.MQTTConfig) config.Config {
	cfg := config.Defaults()
	cfg.RunMode = config.RunModeSplit
	cfg.MQTT = mqttCfg
	cfg.Buffer = testBufferConfig(filepath.Join("data", "test.db"))
	return cfg
}

func testBufferConfig(path string) config.BufferConfig {
	return config.BufferConfig{
		Enabled:                true,
		StorageType:            "sqlite",
		Path:                   path,
		MaxSizeMB:              512,
		TTLHours:               168,
		ResumeRateLimit:        1000,
		ResumeBatchSize:        100,
		CleanupIntervalSeconds: 60,
	}
}

func mqttTestMessage(id string) *dtv1.DeviceMessage {
	return &dtv1.DeviceMessage{
		MessageId: id,
		Timestamp: time.Now().UnixMilli(),
		Direction: dtv1.Direction_UPSTREAM,
		Device: &dtv1.DeviceIdentity{
			DeviceId:    "device-1",
			ConnectorId: "connector-1",
		},
		Type: dtv1.MessageType_TELEMETRY,
		Payload: &dtv1.DeviceMessage_Telemetry{
			Telemetry: &dtv1.TelemetryPayload{},
		},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

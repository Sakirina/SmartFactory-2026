package mqtt

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"github.com/eclipse/paho.golang/autopaho"
	paho5 "github.com/eclipse/paho.golang/paho"
	paho3 "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"
)

func TestMQTTV5SpikeUserPropertiesAndV3Compatibility(t *testing.T) {
	broker, cleanup := startEmbeddedBroker(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	gatewayID := "gateway-v5-spike"
	topic := upstreamTopic(t, gatewayID, dtv1.MessageType_TELEMETRY)
	v5Messages := make(chan *paho5.Publish, 1)
	v5Subscriber := newV5Connection(t, ctx, broker, "v5-subscriber", func(received paho5.PublishReceived) (bool, error) {
		v5Messages <- received.Packet
		return true, nil
	})
	defer disconnectV5(t, v5Subscriber)

	if _, err := v5Subscriber.Subscribe(ctx, &paho5.Subscribe{
		Subscriptions: []paho5.SubscribeOptions{
			{Topic: topic, QoS: 1},
		},
	}); err != nil {
		t.Fatalf("v5 Subscribe returned error: %v", err)
	}

	v3Messages := subscribeV3Raw(t, broker, topic)
	v5Publisher := newV5Connection(t, ctx, broker, "v5-publisher", nil)
	defer disconnectV5(t, v5Publisher)

	payload, err := proto.Marshal(&dtv1.DeviceMessageBatch{
		Messages: []*dtv1.DeviceMessage{mqttTestMessage("msg-v5-spike")},
		BatchId:  "batch-v5-spike",
	})
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	properties := paho5.UserProperties{}
	properties.Add("message_id", "msg-v5-spike")
	properties.Add("command_id", "cmd-v5-spike")
	properties.Add("gateway_id", gatewayID)
	expirySeconds := uint32(168 * 60 * 60)

	response, err := v5Publisher.Publish(ctx, &paho5.Publish{
		Topic:   topic,
		QoS:     1,
		Payload: payload,
		Properties: &paho5.PublishProperties{
			ContentType:     "application/x-protobuf",
			ResponseTopic:   "dt/v1/up/" + gatewayID + "/cmd-response",
			CorrelationData: []byte("cmd-v5-spike"),
			MessageExpiry:   &expirySeconds,
			User:            properties,
		},
	})
	if err != nil {
		t.Fatalf("v5 Publish returned error: %v", err)
	}
	if response == nil {
		t.Fatal("v5 Publish response is nil")
	}

	v5Publish := receiveV5Publish(t, v5Messages)
	if got := string(v5Publish.Payload); got != string(payload) {
		t.Fatalf("v5 payload = %q, want protobuf payload", got)
	}
	if props := v5Publish.Properties; props == nil {
		t.Fatal("v5 publish properties were not forwarded")
	} else {
		assertUserProperty(t, props.User, "message_id", "msg-v5-spike")
		assertUserProperty(t, props.User, "command_id", "cmd-v5-spike")
		assertUserProperty(t, props.User, "gateway_id", gatewayID)
		if props.ResponseTopic != "dt/v1/up/"+gatewayID+"/cmd-response" {
			t.Fatalf("response topic = %q", props.ResponseTopic)
		}
		if !bytes.Equal(props.CorrelationData, []byte("cmd-v5-spike")) {
			t.Fatalf("correlation data = %q", string(props.CorrelationData))
		}
		if props.MessageExpiry == nil {
			t.Fatal("message expiry was not forwarded")
		}
		if got := *props.MessageExpiry; got == 0 || got > expirySeconds {
			t.Fatalf("message expiry = %d, want in range (0, %d]", got, expirySeconds)
		}
		t.Logf("v5 message expiry forwarded as %d seconds from requested %d seconds", *props.MessageExpiry, expirySeconds)
	}

	v3Payload := receiveV3Payload(t, v3Messages)
	var batch dtv1.DeviceMessageBatch
	if err := proto.Unmarshal(v3Payload, &batch); err != nil {
		t.Fatalf("v3 subscriber payload did not decode as protobuf batch: %v", err)
	}
	if got := batch.GetMessages()[0].GetMessageId(); got != "msg-v5-spike" {
		t.Fatalf("v3 subscriber message_id = %q, want msg-v5-spike", got)
	}
}

func newV5Connection(t *testing.T, ctx context.Context, broker string, clientID string, handler func(paho5.PublishReceived) (bool, error)) *autopaho.ConnectionManager {
	t.Helper()
	brokerURL, err := mqttV5URL(broker)
	if err != nil {
		t.Fatalf("mqttV5URL returned error: %v", err)
	}
	handlers := []func(paho5.PublishReceived) (bool, error){}
	if handler != nil {
		handlers = append(handlers, handler)
	}
	manager, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{brokerURL},
		CleanStartOnInitialConnection: true,
		ConnectTimeout:                time.Second,
		KeepAlive:                     10,
		ClientConfig: paho5.ClientConfig{
			ClientID:          fmt.Sprintf("%s-%d", clientID, time.Now().UnixNano()),
			PacketTimeout:     time.Second,
			OnPublishReceived: handlers,
		},
	})
	if err != nil {
		t.Fatalf("NewConnection returned error: %v", err)
	}
	if err := manager.AwaitConnection(ctx); err != nil {
		t.Fatalf("AwaitConnection returned error: %v", err)
	}
	return manager
}

func mqttV5URL(broker string) (*url.URL, error) {
	normalized := broker
	if strings.HasPrefix(normalized, "tcp://") {
		normalized = "mqtt://" + strings.TrimPrefix(normalized, "tcp://")
	}
	return url.Parse(normalized)
}

func disconnectV5(t *testing.T, manager *autopaho.ConnectionManager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Disconnect(ctx); err != nil {
		t.Fatalf("v5 Disconnect returned error: %v", err)
	}
}

func receiveV5Publish(t *testing.T, ch <-chan *paho5.Publish) *paho5.Publish {
	t.Helper()
	select {
	case publish := <-ch:
		return publish
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for v5 publish")
		return nil
	}
}

func subscribeV3Raw(t *testing.T, broker string, topic string) <-chan []byte {
	t.Helper()
	ch := make(chan []byte, 1)
	opts := paho3.NewClientOptions().
		AddBroker(broker).
		SetClientID("v3-subscriber-" + time.Now().Format("150405.000000")).
		SetConnectTimeout(time.Second)
	client := paho3.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(time.Second) || token.Error() != nil {
		t.Fatalf("v3 subscriber connect failed: %v", token.Error())
	}
	t.Cleanup(func() {
		if client.IsConnected() {
			client.Disconnect(100)
		}
	})
	token = client.Subscribe(topic, 1, func(_ paho3.Client, msg paho3.Message) {
		ch <- append([]byte(nil), msg.Payload()...)
	})
	if !token.WaitTimeout(time.Second) || token.Error() != nil {
		t.Fatalf("v3 subscriber subscribe failed: %v", token.Error())
	}
	return ch
}

func receiveV3Payload(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case payload := <-ch:
		return payload
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for v3 payload")
		return nil
	}
}

func assertUserProperty(t *testing.T, properties paho5.UserProperties, key string, want string) {
	t.Helper()
	if got := properties.Get(key); got != want {
		t.Fatalf("user property %s = %q, want %q", key, got, want)
	}
}

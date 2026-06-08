package mqtt

import (
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/proto"
)

func TestTopicsFollowBaseline(t *testing.T) {
	topics := Topics{GatewayID: "gw-1"}

	topic, qos, err := topics.Upstream(dtv1.MessageType_CMD_RESPONSE)
	if err != nil {
		t.Fatalf("Upstream returned error: %v", err)
	}
	if topic != "dt/v1/up/gw-1/cmd-response" {
		t.Fatalf("topic = %q", topic)
	}
	if qos != 2 {
		t.Fatalf("qos = %d, want 2", qos)
	}
	if topics.DownCommand() != "dt/v1/down/gw-1/command" {
		t.Fatalf("down command topic = %q", topics.DownCommand())
	}
	if topics.DownConfig() != "dt/v1/down/gw-1/config" {
		t.Fatalf("down config topic = %q", topics.DownConfig())
	}
}

func TestDeviceMessageBatchPayloadRoundTrip(t *testing.T) {
	batch := &dtv1.DeviceMessageBatch{
		BatchId:   "batch-1",
		CreatedAt: time.Now().UnixMilli(),
		Messages: []*dtv1.DeviceMessage{{
			MessageId: "msg-1",
			Direction: dtv1.Direction_UPSTREAM,
			Type:      dtv1.MessageType_TELEMETRY,
			Payload:   &dtv1.DeviceMessage_Telemetry{Telemetry: &dtv1.TelemetryPayload{}},
		}},
	}
	data, err := proto.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded dtv1.DeviceMessageBatch
	if err := proto.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.GetMessages()[0].GetMessageId() != "msg-1" {
		t.Fatalf("message id = %q", decoded.GetMessages()[0].GetMessageId())
	}
}

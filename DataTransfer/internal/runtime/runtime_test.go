package runtime

import (
	"context"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
)

func TestSubscribeAndPullMessages(t *testing.T) {
	cfg := config.Defaults()
	cfg.Runtime.RingSize = 2
	rt := New(cfg)

	filter := FilterFromSubscribeRequest(&dtv1.SubscribeRequest{
		TagMatch: map[string]string{"workshop": "A"},
	}, []dtv1.MessageType{dtv1.MessageType_TELEMETRY})
	ch, cancel := rt.Subscribe(filter)
	defer cancel()

	if err := rt.Publish(telemetryMessage("msg-1", "B")); err != nil {
		t.Fatalf("publish non-matching message: %v", err)
	}
	if err := rt.Publish(telemetryMessage("msg-2", "A")); err != nil {
		t.Fatalf("publish matching message: %v", err)
	}

	select {
	case msg := <-ch:
		if msg.GetMessageId() != "msg-2" {
			t.Fatalf("received %q, want msg-2", msg.GetMessageId())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription message")
	}

	batch := rt.Pull(&dtv1.PullRequest{MaxCount: 1, Types: []dtv1.MessageType{dtv1.MessageType_TELEMETRY}})
	if len(batch.GetMessages()) != 1 {
		t.Fatalf("pull returned %d messages, want 1", len(batch.GetMessages()))
	}
}

func TestCommandHandlingRejectsAndDeduplicates(t *testing.T) {
	rt := New(config.Defaults())
	cmd := controlCommand("cmd-1")

	response, duplicate, err := rt.HandleCommand(context.Background(), cmd)
	if err != nil {
		t.Fatalf("HandleCommand returned error: %v", err)
	}
	if duplicate {
		t.Fatal("first command should not be duplicate")
	}
	if response.GetStatus() != dtv1.CommandStatus_REJECTED {
		t.Fatalf("status = %s, want REJECTED", response.GetStatus())
	}

	_, duplicate, err = rt.HandleCommand(context.Background(), cmd)
	if err != nil {
		t.Fatalf("duplicate HandleCommand returned error: %v", err)
	}
	if !duplicate {
		t.Fatal("second command should be duplicate")
	}
}

func TestAsyncCommandPublishesCommandResponse(t *testing.T) {
	rt := New(config.Defaults())
	ch, cancel := rt.Subscribe(Filter{Types: map[dtv1.MessageType]struct{}{dtv1.MessageType_CMD_RESPONSE: {}}})
	defer cancel()

	accepted, err := rt.AcceptCommandAsync(context.Background(), controlCommand("cmd-async"))
	if err != nil {
		t.Fatalf("AcceptCommandAsync returned error: %v", err)
	}
	if accepted.GetCommandId() != "cmd-async" {
		t.Fatalf("accepted command id = %q", accepted.GetCommandId())
	}

	select {
	case msg := <-ch:
		if msg.GetType() != dtv1.MessageType_CMD_RESPONSE {
			t.Fatalf("message type = %s", msg.GetType())
		}
		if msg.GetCommandId() != "cmd-async" {
			t.Fatalf("command id = %q", msg.GetCommandId())
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async command response")
	}
}

func telemetryMessage(id, workshop string) *dtv1.DeviceMessage {
	return &dtv1.DeviceMessage{
		MessageId: id,
		Timestamp: time.Now().UnixMilli(),
		Direction: dtv1.Direction_UPSTREAM,
		Device: &dtv1.DeviceIdentity{
			DeviceId: "device-1",
			Tags: map[string]string{
				"workshop": workshop,
			},
		},
		Type: dtv1.MessageType_TELEMETRY,
		Payload: &dtv1.DeviceMessage_Telemetry{
			Telemetry: &dtv1.TelemetryPayload{},
		},
	}
}

func controlCommand(commandID string) *dtv1.DeviceMessage {
	return &dtv1.DeviceMessage{
		MessageId: "msg-" + commandID,
		Timestamp: time.Now().UnixMilli(),
		Direction: dtv1.Direction_DOWNSTREAM,
		Device: &dtv1.DeviceIdentity{
			DeviceId: "device-1",
		},
		Type:      dtv1.MessageType_CONTROL,
		CommandId: commandID,
		Payload: &dtv1.DeviceMessage_Control{
			Control: &dtv1.ControlPayload{Action: "start"},
		},
	}
}

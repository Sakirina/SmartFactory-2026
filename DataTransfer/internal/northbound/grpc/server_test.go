package grpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	grpcadapter "competition2026/product/datatransfer/internal/northbound/grpc"
	dtruntime "competition2026/product/datatransfer/internal/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestSendCommandAndDuplicate(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	ctx := context.Background()
	cmd := grpcControlCommand("cmd-1")
	response, err := client.SendCommand(ctx, cmd)
	if err != nil {
		t.Fatalf("SendCommand returned error: %v", err)
	}
	if response.GetStatus() != dtv1.CommandStatus_REJECTED {
		t.Fatalf("status = %s, want REJECTED", response.GetStatus())
	}

	duplicate, err := client.SendCommand(ctx, cmd)
	if err != nil {
		t.Fatalf("duplicate SendCommand returned error: %v", err)
	}
	if duplicate.GetStatus() != dtv1.CommandStatus_REJECTED {
		t.Fatalf("duplicate status = %s, want REJECTED", duplicate.GetStatus())
	}
}

func TestSubscribeEventsReceivesAsyncCommandResponse(t *testing.T) {
	client, cleanup := newClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := client.SubscribeEvents(ctx, &dtv1.SubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeEvents returned error: %v", err)
	}

	received := make(chan *dtv1.DeviceMessage, 1)
	recvErr := make(chan error, 1)
	go func() {
		msg, err := stream.Recv()
		if err != nil {
			recvErr <- err
			return
		}
		received <- msg
	}()
	time.Sleep(10 * time.Millisecond)

	accepted, err := client.SendCommandAsync(ctx, grpcControlCommand("cmd-async"))
	if err != nil {
		t.Fatalf("SendCommandAsync returned error: %v", err)
	}
	if accepted.GetCommandId() != "cmd-async" {
		t.Fatalf("accepted command id = %q", accepted.GetCommandId())
	}

	var msg *dtv1.DeviceMessage
	select {
	case err := <-recvErr:
		t.Fatalf("Recv returned error: %v", err)
	case msg = <-received:
	case <-ctx.Done():
		t.Fatalf("Recv timed out: %v", ctx.Err())
	}
	if msg.GetType() != dtv1.MessageType_CMD_RESPONSE {
		t.Fatalf("message type = %s, want CMD_RESPONSE", msg.GetType())
	}
	if msg.GetCommandId() != "cmd-async" {
		t.Fatalf("command id = %q", msg.GetCommandId())
	}
}

func TestPullMessages(t *testing.T) {
	client, cleanup, rt := newClientWithRuntime(t)
	defer cleanup()

	if err := rt.Publish(&dtv1.DeviceMessage{
		MessageId: "msg-pull",
		Timestamp: time.Now().UnixMilli(),
		Direction: dtv1.Direction_UPSTREAM,
		Type:      dtv1.MessageType_TELEMETRY,
		Payload:   &dtv1.DeviceMessage_Telemetry{Telemetry: &dtv1.TelemetryPayload{}},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	batch, err := client.PullMessages(context.Background(), &dtv1.PullRequest{MaxCount: 10})
	if err != nil {
		t.Fatalf("PullMessages returned error: %v", err)
	}
	if len(batch.GetMessages()) != 1 {
		t.Fatalf("messages = %d, want 1", len(batch.GetMessages()))
	}
}

func newClient(t *testing.T) (dtv1.DataTransferServiceClient, func()) {
	client, cleanup, _ := newClientWithRuntime(t)
	return client, cleanup
}

func newClientWithRuntime(t *testing.T) (dtv1.DataTransferServiceClient, func(), *dtruntime.Runtime) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	rt := dtruntime.New(config.Defaults())
	grpcadapter.Register(server, rt)
	go func() {
		_ = server.Serve(listener)
	}()

	ctx := context.Background()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		server.Stop()
		_ = listener.Close()
	}
	_ = ctx
	return dtv1.NewDataTransferServiceClient(conn), cleanup, rt
}

func grpcControlCommand(commandID string) *dtv1.DeviceMessage {
	return &dtv1.DeviceMessage{
		MessageId: "msg-" + commandID,
		Timestamp: time.Now().UnixMilli(),
		Direction: dtv1.Direction_DOWNSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "device-1"},
		Type:      dtv1.MessageType_CONTROL,
		CommandId: commandID,
		Payload: &dtv1.DeviceMessage_Control{
			Control: &dtv1.ControlPayload{Action: "start"},
		},
	}
}

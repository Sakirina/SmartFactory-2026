package sidecar

import (
	"context"
	"net"
	"testing"
	"time"

	pluginv1 "competition2026/product/datatransfer/gen/datatransfer/plugin/v1"
	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func TestMockSidecarLifecycleCommandAndStream(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	mock := NewMockServer()
	RegisterMock(server, mock)
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()
	client := pluginv1.NewSidecarConnectorServiceClient(conn)

	initResp, err := client.Init(ctx, &pluginv1.InitRequest{ConnectorId: "sidecar-1", Protocol: "sidecar", ConfigJson: []byte(`{"ok":true}`)})
	if err != nil || !initResp.GetSuccess() {
		t.Fatalf("Init = (%v, %v), want success", initResp, err)
	}
	stream, err := client.Start(ctx, &pluginv1.StartRequest{ConnectorId: "sidecar-1"})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	mock.Emit(&dtv1.DeviceMessage{MessageId: "sidecar-msg-1", Direction: dtv1.Direction_UPSTREAM, Type: dtv1.MessageType_EVENT})
	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if msg.GetMessageId() != "sidecar-msg-1" {
		t.Fatalf("message_id = %q, want sidecar-msg-1", msg.GetMessageId())
	}
	commandResp, err := client.SendCommand(ctx, &pluginv1.CommandRequest{Command: &dtv1.DeviceMessage{CommandId: "cmd-1"}})
	if err != nil || commandResp.GetStatus() != dtv1.CommandStatus_SUCCESS {
		t.Fatalf("SendCommand = (%v, %v), want success", commandResp, err)
	}
	reloadResp, err := client.ReloadConfig(ctx, &pluginv1.ReloadConfigRequest{ConnectorId: "sidecar-1", ConfigJson: []byte(`{"reload":true}`)})
	if err != nil || !reloadResp.GetSuccess() {
		t.Fatalf("ReloadConfig = (%v, %v), want success", reloadResp, err)
	}
	status, err := client.GetStatus(ctx, &pluginv1.StatusRequest{ConnectorId: "sidecar-1"})
	if err != nil || status.GetState() != "running" {
		t.Fatalf("GetStatus = (%v, %v), want running", status, err)
	}
	if _, err := client.Stop(ctx, &pluginv1.StopRequest{ConnectorId: "sidecar-1"}); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
}

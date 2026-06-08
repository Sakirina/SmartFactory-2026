package grpc_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	connectorpkg "competition2026/product/datatransfer/internal/connector"
	modbusconnector "competition2026/product/datatransfer/internal/connector/modbus"
	grpcadapter "competition2026/product/datatransfer/internal/northbound/grpc"
	dtruntime "competition2026/product/datatransfer/internal/runtime"
	smodbus "github.com/simonvetter/modbus"
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

func TestModbusConnectorThroughGRPC(t *testing.T) {
	modbusClient := newGRPCFakeModbusClient()
	modbusClient.coils[1] = true
	modbusClient.holding[20] = 100
	connectorpkg.Register(modbusconnector.Protocol, func() connectorpkg.Connector {
		return modbusconnector.NewConnectorWithClientFactory(func(config.ConnectionConfig) (modbusconnector.Client, error) {
			return modbusClient, nil
		})
	})

	rt := dtruntime.New(config.Defaults())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := connectorpkg.NewManager([]config.ConnectorConfig{grpcModbusConfig()}, rt, logger)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	rt.AttachConnectorManager(manager)

	client, cleanup := newClientForRuntime(t, rt)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := client.SubscribeTelemetry(ctx, &dtv1.SubscribeRequest{DeviceIds: []string{"device-grpc"}})
	if err != nil {
		t.Fatalf("SubscribeTelemetry returned error: %v", err)
	}

	managerCtx, managerCancel := context.WithCancel(context.Background())
	defer managerCancel()
	go func() {
		_ = manager.Start(managerCtx)
	}()

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv returned error: %v", err)
	}
	if msg.GetType() != dtv1.MessageType_TELEMETRY {
		t.Fatalf("message type = %s, want TELEMETRY", msg.GetType())
	}

	response, err := client.SendCommand(context.Background(), &dtv1.DeviceMessage{
		Direction: dtv1.Direction_DOWNSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "device-grpc"},
		Type:      dtv1.MessageType_CONTROL,
		CommandId: "cmd-grpc-speed",
		Payload: &dtv1.DeviceMessage_Control{
			Control: &dtv1.ControlPayload{
				Action: "set_speed",
				Params: map[string]string{"speed": "321"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SendCommand returned error: %v", err)
	}
	if response.GetStatus() != dtv1.CommandStatus_SUCCESS {
		t.Fatalf("status = %s, want SUCCESS", response.GetStatus())
	}
	if got := modbusClient.registerValue(20); got != 321 {
		t.Fatalf("holding register 20 = %d, want 321", got)
	}
}

func newClient(t *testing.T) (dtv1.DataTransferServiceClient, func()) {
	client, cleanup, _ := newClientWithRuntime(t)
	return client, cleanup
}

func newClientWithRuntime(t *testing.T) (dtv1.DataTransferServiceClient, func(), *dtruntime.Runtime) {
	t.Helper()
	rt := dtruntime.New(config.Defaults())
	client, cleanup := newClientForRuntime(t, rt)
	return client, cleanup, rt
}

func newClientForRuntime(t *testing.T, rt *dtruntime.Runtime) (dtv1.DataTransferServiceClient, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
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
	return dtv1.NewDataTransferServiceClient(conn), cleanup
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

type grpcFakeModbusClient struct {
	mu      sync.Mutex
	coils   map[uint16]bool
	holding map[uint16]uint16
}

func newGRPCFakeModbusClient() *grpcFakeModbusClient {
	return &grpcFakeModbusClient{
		coils:   map[uint16]bool{},
		holding: map[uint16]uint16{},
	}
}

func (c *grpcFakeModbusClient) Open() error  { return nil }
func (c *grpcFakeModbusClient) Close() error { return nil }
func (c *grpcFakeModbusClient) SetUnitId(uint8) error {
	return nil
}

func (c *grpcFakeModbusClient) ReadCoil(addr uint16) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.coils[addr], nil
}

func (c *grpcFakeModbusClient) ReadDiscreteInput(uint16) (bool, error) {
	return false, nil
}

func (c *grpcFakeModbusClient) ReadRegisters(addr uint16, quantity uint16, _ smodbus.RegType) ([]uint16, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	values := make([]uint16, 0, quantity)
	for offset := uint16(0); offset < quantity; offset++ {
		values = append(values, c.holding[addr+offset])
	}
	return values, nil
}

func (c *grpcFakeModbusClient) WriteCoil(addr uint16, value bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.coils[addr] = value
	return nil
}

func (c *grpcFakeModbusClient) WriteCoils(addr uint16, values []bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for idx, value := range values {
		c.coils[addr+uint16(idx)] = value
	}
	return nil
}

func (c *grpcFakeModbusClient) WriteRegister(addr uint16, value uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.holding[addr] = value
	return nil
}

func (c *grpcFakeModbusClient) WriteRegisters(addr uint16, values []uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for idx, value := range values {
		c.holding[addr+uint16(idx)] = value
	}
	return nil
}

func (c *grpcFakeModbusClient) registerValue(addr uint16) uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.holding[addr]
}

func grpcModbusConfig() config.ConnectorConfig {
	return config.ConnectorConfig{
		ConnectorID: "modbus-grpc",
		Protocol:    modbusconnector.Protocol,
		Connection: config.ConnectionConfig{
			UnitID:        1,
			TimeoutMillis: 1000,
		},
		Polling: config.PollingConfig{
			IntervalMillis: 1000,
			TimeoutMillis:  1000,
		},
		ActionMappings: map[string]config.ActionMapping{
			"set_speed": {
				Type:     "write_single_register",
				Address:  20,
				DataType: modbusconnector.DataTypeUint16,
				Param:    "speed",
			},
		},
		Devices: []config.DeviceConfig{
			{
				DeviceID:   "device-grpc",
				DeviceName: "gRPC Modbus Device",
				DeviceType: "plc",
				UnitID:     1,
				Datapoints: []config.DatapointConfig{
					{
						Key:          "running",
						RegisterType: modbusconnector.RegisterTypeCoil,
						Address:      1,
						DataType:     modbusconnector.DataTypeBool,
					},
					{
						Key:          "speed",
						RegisterType: modbusconnector.RegisterTypeHoldingRegister,
						Address:      20,
						DataType:     modbusconnector.DataTypeUint16,
					},
				},
			},
		},
	}
}

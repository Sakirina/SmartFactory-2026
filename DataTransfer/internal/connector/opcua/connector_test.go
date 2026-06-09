package opcua

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
)

func TestConnectorSubscriptionQueryWriteAndCall(t *testing.T) {
	fake := newFakeClient()
	conn := NewConnectorWithClientFactory(func(config.ConnectorConfig) (Client, error) {
		return fake, nil
	})
	cfg := config.ConnectorConfig{
		ConnectorID: "opcua-1",
		Protocol:    Protocol,
		Connection: config.ConnectionConfig{
			URL: "opc.tcp://127.0.0.1:4840",
		},
		ActionMappings: map[string]config.ActionMapping{
			"set_speed": {Type: "write", NodeID: "ns=2;s=SpeedSetpoint", Param: "speed"},
			"reset":     {Type: "call", NodeID: "ns=2;s=Machine", MethodID: "ns=2;s=Reset"},
		},
		Devices: []config.DeviceConfig{{
			DeviceID: "opc-device-1",
			Datapoints: []config.DatapointConfig{
				{Key: "temperature", NodeID: "ns=2;s=Temperature", DataType: "float64", Unit: "C"},
				{Key: "running", NodeID: "ns=2;s=Running", DataType: "bool"},
			},
			ActionMappings: map[string]config.ActionMapping{
				"mode": {Type: "write", NodeID: "ns=2;s=Mode"},
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
	fake.waitConnected(t)

	fake.emit("ns=2;s=Temperature", 23.4)
	msg := receiveOPCUAMessage(t, upstream)
	if msg.GetType() != dtv1.MessageType_TELEMETRY {
		t.Fatalf("type = %s, want telemetry", msg.GetType())
	}
	dp := msg.GetTelemetry().GetDatapoints()[0]
	if dp.GetKey() != "temperature" || dp.GetValue().GetDoubleValue() != 23.4 {
		t.Fatalf("datapoint = %+v, want temperature 23.4", dp)
	}

	fake.setRead("ns=2;s=Running", true)
	queryResp, err := conn.SendCommand(context.Background(), &dtv1.DeviceMessage{
		CommandId: "query-1",
		Direction: dtv1.Direction_DOWNSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "opc-device-1"},
		Type:      dtv1.MessageType_QUERY,
		Payload: &dtv1.DeviceMessage_Query{Query: &dtv1.QueryPayload{
			Keys: []string{"running"},
		}},
	})
	if err != nil || queryResp.GetStatus() != dtv1.CommandStatus_SUCCESS {
		t.Fatalf("query response = (%v, %v), want success", queryResp, err)
	}
	if queryResp.GetResult()["running"] != "true" {
		t.Fatalf("query result = %+v, want running=true", queryResp.GetResult())
	}

	writeResp, err := conn.SendCommand(context.Background(), &dtv1.DeviceMessage{
		CommandId: "param-1",
		Direction: dtv1.Direction_DOWNSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "opc-device-1"},
		Type:      dtv1.MessageType_PARAM_UPDATE,
		Payload: &dtv1.DeviceMessage_ParamUpdate{ParamUpdate: &dtv1.ParamUpdatePayload{
			Params: []*dtv1.ParamEntry{{Key: "mode", Value: &dtv1.DataValue{Kind: &dtv1.DataValue_StringValue{StringValue: "auto"}}}},
		}},
	})
	if err != nil || writeResp.GetStatus() != dtv1.CommandStatus_SUCCESS {
		t.Fatalf("write response = (%v, %v), want success", writeResp, err)
	}
	if got := fake.writtenValue("ns=2;s=Mode"); got != "auto" {
		t.Fatalf("written value = %v, want auto", got)
	}

	callResp, err := conn.SendCommand(context.Background(), &dtv1.DeviceMessage{
		CommandId: "control-1",
		Direction: dtv1.Direction_DOWNSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "opc-device-1"},
		Type:      dtv1.MessageType_CONTROL,
		Payload: &dtv1.DeviceMessage_Control{Control: &dtv1.ControlPayload{
			Action: "reset",
			Params: map[string]string{"reason": "test"},
		}},
	})
	if err != nil || callResp.GetStatus() != dtv1.CommandStatus_SUCCESS {
		t.Fatalf("call response = (%v, %v), want success", callResp, err)
	}
	if objectID, methodID := fake.lastCall(); objectID != "ns=2;s=Machine" || methodID != "ns=2;s=Reset" {
		t.Fatalf("last call = (%q, %q), want reset method", objectID, methodID)
	}
}

func TestNativeClientExternalSmoke(t *testing.T) {
	endpoint := os.Getenv("DT_OPCUA_SMOKE_ENDPOINT")
	if endpoint == "" {
		t.Skip("DT_OPCUA_SMOKE_ENDPOINT is not set")
	}
	client, err := nativeClientFactory(config.ConnectorConfig{
		ConnectorID: "opcua-smoke",
		Protocol:    Protocol,
		Connection: config.ConnectionConfig{
			URL: endpoint,
		},
	})
	if err != nil {
		t.Fatalf("nativeClientFactory returned error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

type fakeClient struct {
	mu          sync.Mutex
	connected   chan struct{}
	emitFn      func(string, any)
	reads       map[string]any
	writes      map[string]any
	callObject  string
	callMethod  string
	closed      bool
	connectOnce sync.Once
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		connected: make(chan struct{}),
		reads:     map[string]any{"ns=2;s=Temperature": 23.4},
		writes:    map[string]any{},
	}
}

func (f *fakeClient) Connect(context.Context) error {
	f.connectOnce.Do(func() { close(f.connected) })
	return nil
}

func (f *fakeClient) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeClient) Read(_ context.Context, nodeID string) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.reads[nodeID]
	if !ok {
		return nil, errors.New("node not found")
	}
	return value, nil
}

func (f *fakeClient) Write(_ context.Context, nodeID string, value any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes[nodeID] = value
	return nil
}

func (f *fakeClient) Call(_ context.Context, objectID string, methodID string, _ []any) ([]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callObject = objectID
	f.callMethod = methodID
	return []any{"ok"}, nil
}

func (f *fakeClient) Subscribe(ctx context.Context, _ []string, emit func(nodeID string, value any)) error {
	f.mu.Lock()
	f.emitFn = emit
	f.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeClient) waitConnected(t *testing.T) {
	t.Helper()
	select {
	case <-f.connected:
	case <-time.After(time.Second):
		t.Fatal("opcua fake client did not connect")
	}
}

func (f *fakeClient) emit(nodeID string, value any) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		emit := f.emitFn
		f.mu.Unlock()
		if emit != nil {
			emit(nodeID, value)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *fakeClient) setRead(nodeID string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads[nodeID] = value
}

func (f *fakeClient) writtenValue(nodeID string) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes[nodeID]
}

func (f *fakeClient) lastCall() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callObject, f.callMethod
}

func receiveOPCUAMessage(t *testing.T, ch <-chan *dtv1.DeviceMessage) *dtv1.DeviceMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for opcua message")
		return nil
	}
}

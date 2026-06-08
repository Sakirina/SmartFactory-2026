package modbus

import (
	"context"
	"sync"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	smodbus "github.com/simonvetter/modbus"
)

type fakeClient struct {
	mu             sync.Mutex
	opened         int
	closed         int
	unitIDs        []uint8
	coils          map[uint16]bool
	discreteInputs map[uint16]bool
	holding        map[uint16]uint16
	input          map[uint16]uint16
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		coils:          map[uint16]bool{},
		discreteInputs: map[uint16]bool{},
		holding:        map[uint16]uint16{},
		input:          map[uint16]uint16{},
	}
}

func (c *fakeClient) Open() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opened++
	return nil
}

func (c *fakeClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

func (c *fakeClient) SetUnitId(id uint8) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unitIDs = append(c.unitIDs, id)
	return nil
}

func (c *fakeClient) ReadCoil(addr uint16) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.coils[addr], nil
}

func (c *fakeClient) ReadDiscreteInput(addr uint16) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.discreteInputs[addr], nil
}

func (c *fakeClient) ReadRegisters(addr uint16, quantity uint16, regType smodbus.RegType) ([]uint16, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	source := c.holding
	if regType == smodbus.INPUT_REGISTER {
		source = c.input
	}
	values := make([]uint16, 0, quantity)
	for offset := uint16(0); offset < quantity; offset++ {
		values = append(values, source[addr+offset])
	}
	return values, nil
}

func (c *fakeClient) WriteCoil(addr uint16, value bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.coils[addr] = value
	return nil
}

func (c *fakeClient) WriteCoils(addr uint16, values []bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for idx, value := range values {
		c.coils[addr+uint16(idx)] = value
	}
	return nil
}

func (c *fakeClient) WriteRegister(addr uint16, value uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.holding[addr] = value
	return nil
}

func (c *fakeClient) WriteRegisters(addr uint16, values []uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for idx, value := range values {
		c.holding[addr+uint16(idx)] = value
	}
	return nil
}

func TestConnectorPollsTelemetry(t *testing.T) {
	client := newFakeClient()
	client.coils[1] = true
	client.holding[10] = 235

	connector := NewConnectorWithClientFactory(func(config.ConnectionConfig) (Client, error) {
		return client, nil
	})
	if err := connector.Init(testConnectorConfig()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := make(chan *dtv1.DeviceMessage, 8)
	done := make(chan error, 1)
	go func() {
		done <- connector.Start(ctx, upstream)
	}()

	msg := waitForMessage(t, upstream, dtv1.MessageType_TELEMETRY)
	if msg.GetDevice().GetDeviceId() != "device-1" {
		t.Fatalf("device id = %q", msg.GetDevice().GetDeviceId())
	}
	if len(msg.GetTelemetry().GetDatapoints()) != 3 {
		t.Fatalf("datapoints = %d, want 3", len(msg.GetTelemetry().GetDatapoints()))
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for connector shutdown")
	}
}

func TestConnectorExecutesMappedCommandsAndQuery(t *testing.T) {
	client := newFakeClient()
	client.holding[20] = 100

	connector := NewConnectorWithClientFactory(func(config.ConnectionConfig) (Client, error) {
		return client, nil
	})
	if err := connector.Init(testConnectorConfig()); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	response, err := connector.SendCommand(context.Background(), &dtv1.DeviceMessage{
		Direction: dtv1.Direction_DOWNSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "device-1"},
		Type:      dtv1.MessageType_CONTROL,
		CommandId: "cmd-speed",
		Payload: &dtv1.DeviceMessage_Control{
			Control: &dtv1.ControlPayload{
				Action: "set_speed",
				Params: map[string]string{"speed": "120"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SendCommand returned error: %v", err)
	}
	if response.GetStatus() != dtv1.CommandStatus_SUCCESS {
		t.Fatalf("status = %s, want SUCCESS", response.GetStatus())
	}
	if got := client.registerValue(20); got != 120 {
		t.Fatalf("holding register 20 = %d, want 120", got)
	}

	query, err := connector.SendCommand(context.Background(), &dtv1.DeviceMessage{
		Direction: dtv1.Direction_DOWNSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: "device-1"},
		Type:      dtv1.MessageType_QUERY,
		CommandId: "cmd-query",
		Payload: &dtv1.DeviceMessage_Query{
			Query: &dtv1.QueryPayload{
				QueryType: dtv1.QueryType_READ_CURRENT,
				Keys:      []string{"speed"},
			},
		},
	})
	if err != nil {
		t.Fatalf("query SendCommand returned error: %v", err)
	}
	if query.GetResult()["speed"] != "120" {
		t.Fatalf("query result = %+v, want speed 120", query.GetResult())
	}
}

func (c *fakeClient) registerValue(addr uint16) uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.holding[addr]
}

func waitForMessage(t *testing.T, ch <-chan *dtv1.DeviceMessage, messageType dtv1.MessageType) *dtv1.DeviceMessage {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case msg := <-ch:
			if msg.GetType() == messageType {
				return msg
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", messageType)
		}
	}
}

func testConnectorConfig() config.ConnectorConfig {
	scale := 0.1
	return config.ConnectorConfig{
		ConnectorID: "modbus-1",
		Protocol:    Protocol,
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
				DataType: DataTypeUint16,
				Param:    "speed",
			},
		},
		Devices: []config.DeviceConfig{
			{
				DeviceID:   "device-1",
				DeviceName: "Demo PLC",
				DeviceType: "plc",
				UnitID:     1,
				Datapoints: []config.DatapointConfig{
					{
						Key:          "running",
						RegisterType: RegisterTypeCoil,
						Address:      1,
						DataType:     DataTypeBool,
					},
					{
						Key:          "temperature",
						RegisterType: RegisterTypeHoldingRegister,
						Address:      10,
						DataType:     DataTypeInt16,
						Scale:        &scale,
						Unit:         "celsius",
					},
					{
						Key:          "speed",
						RegisterType: RegisterTypeHoldingRegister,
						Address:      20,
						DataType:     DataTypeUint16,
					},
				},
			},
		},
	}
}

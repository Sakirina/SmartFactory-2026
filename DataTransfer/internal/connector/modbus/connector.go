// Package modbus 实现 Modbus TCP Connector(设计 4.3 首个内置协议):
// 周期轮询读取线圈/寄存器、双向 Converter(解码遥测、编码下行写操作)、
// 设备状态跟踪与单连接串行访问。poll 与 SendCommand 经 opMu 互斥。
package modbus

import (
	"context"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	dterrors "competition2026/product/datatransfer/internal/errors"
	smodbus "github.com/simonvetter/modbus"
	"google.golang.org/protobuf/proto"
)

const Protocol = "modbus_tcp"

type Client interface {
	Open() error
	Close() error
	SetUnitId(id uint8) error
	ReadCoil(addr uint16) (bool, error)
	ReadDiscreteInput(addr uint16) (bool, error)
	ReadRegisters(addr uint16, quantity uint16, regType smodbus.RegType) ([]uint16, error)
	WriteCoil(addr uint16, value bool) error
	WriteCoils(addr uint16, values []bool) error
	WriteRegister(addr uint16, value uint16) error
	WriteRegisters(addr uint16, values []uint16) error
}

type ClientFactory func(config.ConnectionConfig) (Client, error)

type Connector struct {
	clientFactory ClientFactory

	mu            sync.RWMutex
	opMu          sync.Mutex
	cfg           config.ConnectorConfig
	converter     *Converter
	client        Client
	clientOpen    bool
	status        connector.Status
	devices       []*dtv1.DeviceInfo
	deviceConfigs map[string]config.DeviceConfig
	startedAt     time.Time
}

func init() {
	connector.Register(Protocol, func() connector.Connector {
		return NewConnector()
	})
}

func NewConnector() *Connector {
	return NewConnectorWithClientFactory(nativeClientFactory)
}

func NewConnectorWithClientFactory(factory ClientFactory) *Connector {
	return &Connector{clientFactory: factory}
}

func (c *Connector) Init(cfg config.ConnectorConfig) error {
	if strings.ToLower(cfg.Protocol) != Protocol {
		return fmt.Errorf("%s: unsupported protocol %q", dterrors.CodeConnectorInvalid, cfg.Protocol)
	}
	if c.clientFactory == nil {
		c.clientFactory = nativeClientFactory
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.cfg = cfg
	c.converter = NewConverter(cfg)
	c.status = connector.NewStatus(cfg.ConnectorID, cfg.Protocol)
	c.status.DeviceCount = len(cfg.Devices)
	c.devices = make([]*dtv1.DeviceInfo, 0, len(cfg.Devices))
	c.deviceConfigs = make(map[string]config.DeviceConfig, len(cfg.Devices))
	for _, device := range cfg.Devices {
		c.deviceConfigs[device.DeviceID] = device
		tags := mergeTags(cfg.DefaultTags, device.Tags)
		info := connector.DeviceInfoFromConfig(cfg.ConnectorID, cfg.Protocol, tags, device, dtv1.DeviceState_OFFLINE, now)
		c.devices = append(c.devices, info)
	}
	return nil
}

func (c *Connector) Start(ctx context.Context, upstream chan<- *dtv1.DeviceMessage) error {
	c.setState(connector.StateInitializing, "")
	c.mu.Lock()
	c.startedAt = time.Now()
	intervalMillis := c.cfg.Polling.IntervalMillis
	c.mu.Unlock()
	interval := time.Duration(intervalMillis) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	c.poll(ctx, upstream)
	for {
		select {
		case <-ctx.Done():
			c.setState(connector.StateStopped, "")
			return nil
		case <-ticker.C:
			c.poll(ctx, upstream)
		}
	}
}

func (c *Connector) SendCommand(ctx context.Context, cmd *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.ensureClient(); err != nil {
		return nil, err
	}
	deviceID := cmd.GetDevice().GetDeviceId()
	device, ok := c.deviceConfig(deviceID)
	if !ok {
		return rejectedResponse(cmd, dterrors.CodeCommandNoRoute, "device is not managed by this connector"), nil
	}
	if err := c.setUnit(device); err != nil {
		return nil, err
	}
	switch cmd.GetType() {
	case dtv1.MessageType_CONTROL:
		return c.handleControl(cmd, device)
	case dtv1.MessageType_PARAM_UPDATE:
		return c.handleParamUpdate(cmd, device)
	case dtv1.MessageType_QUERY:
		return c.handleQuery(ctx, cmd, device)
	default:
		return rejectedResponse(cmd, dterrors.CodeCommandUnsupported, "unsupported command type"), nil
	}
}

func (c *Connector) Stop() error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.setState(connector.StateStopped, "")
	return c.closeClientLocked()
}

func (c *Connector) Status() connector.Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	status := c.status
	if !c.startedAt.IsZero() {
		status.Uptime = int64(time.Since(c.startedAt).Seconds())
	}
	return status
}

func (c *Connector) Devices() []*dtv1.DeviceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*dtv1.DeviceInfo, 0, len(c.devices))
	for _, device := range c.devices {
		out = append(out, proto.Clone(device).(*dtv1.DeviceInfo))
	}
	return out
}

// ReloadConfig 与 poll/SendCommand 串行(opMu),并关闭旧连接,
// 使连接参数变更在下一次操作时以新配置重建客户端。
func (c *Connector) ReloadConfig(cfg config.ConnectorConfig) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.closeClientLocked(); err != nil {
		// 旧连接关闭失败不阻塞配置应用,记录后继续。
		c.markError(err)
	}
	return c.Init(cfg)
}

func (c *Connector) poll(ctx context.Context, upstream chan<- *dtv1.DeviceMessage) {
	if ctx.Err() != nil {
		return
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.ensureClient(); err != nil {
		c.markError(err)
		c.markAllDevices(ctx, dtv1.DeviceState_OFFLINE, upstream)
		return
	}
	for _, device := range c.snapshotDeviceConfigs() {
		if err := c.setUnit(device); err != nil {
			c.markError(err)
			_ = c.closeClientLocked()
			c.markDeviceState(ctx, device.DeviceID, dtv1.DeviceState_ERROR, upstream)
			continue
		}
		readings := make([]Reading, 0, len(device.Datapoints))
		readFailed := false
		for _, datapoint := range device.Datapoints {
			raw, err := c.readDatapoint(datapoint)
			if err != nil {
				c.markError(err)
				_ = c.closeClientLocked()
				c.markDeviceState(ctx, device.DeviceID, dtv1.DeviceState_ERROR, upstream)
				readFailed = true
				break
			}
			readings = append(readings, Reading{
				Datapoint: datapoint,
				Raw:       raw,
				Timestamp: time.Now().UnixMilli(),
			})
		}
		if readFailed {
			continue
		}
		if len(readings) == 0 {
			continue
		}
		msg, skipped, err := c.converter.BuildTelemetry(device, readings)
		for range skipped {
			c.addError()
		}
		if err != nil {
			c.markError(err)
			continue
		}
		if msg == nil {
			continue
		}
		c.markDeviceState(ctx, device.DeviceID, dtv1.DeviceState_ONLINE, upstream)
		c.addMessageIn()
		select {
		case upstream <- msg:
		case <-ctx.Done():
			return
		}
	}
	c.setState(connector.StateRunning, "")
}

func (c *Connector) addError() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Stats.ErrorsTotal++
}

func (c *Connector) readDatapoint(datapoint config.DatapointConfig) (any, error) {
	switch strings.ToLower(datapoint.RegisterType) {
	case RegisterTypeCoil:
		return c.client.ReadCoil(datapoint.Address)
	case RegisterTypeDiscreteInput:
		return c.client.ReadDiscreteInput(datapoint.Address)
	case RegisterTypeHoldingRegister:
		return c.client.ReadRegisters(datapoint.Address, registersQuantity(datapoint), smodbus.HOLDING_REGISTER)
	case RegisterTypeInputRegister:
		return c.client.ReadRegisters(datapoint.Address, registersQuantity(datapoint), smodbus.INPUT_REGISTER)
	default:
		return nil, fmt.Errorf("%s: unsupported register_type %q", dterrors.CodeConverterInvalid, datapoint.RegisterType)
	}
}

func (c *Connector) handleControl(cmd *dtv1.DeviceMessage, device config.DeviceConfig) (*dtv1.CommandResponsePayload, error) {
	payload := cmd.GetControl()
	mapping, ok := c.actionMapping(device, payload.GetAction())
	if !ok {
		return rejectedResponse(cmd, dterrors.CodeCommandUnsupported, "action mapping not found"), nil
	}
	value, err := mappingValue(mapping, payload.GetParams())
	if err != nil {
		return rejectedResponse(cmd, dterrors.CodeCommandInvalid, err.Error()), nil
	}
	if err := c.executeMapping(mapping, value); err != nil {
		_ = c.closeClientLocked()
		return nil, err
	}
	c.addMessageOut()
	return successResponse(cmd, map[string]string{"action": payload.GetAction()}), nil
}

func (c *Connector) handleParamUpdate(cmd *dtv1.DeviceMessage, device config.DeviceConfig) (*dtv1.CommandResponsePayload, error) {
	result := make(map[string]string)
	for _, param := range cmd.GetParamUpdate().GetParams() {
		mapping, ok := c.actionMapping(device, param.GetKey())
		if !ok {
			return rejectedResponse(cmd, dterrors.CodeCommandUnsupported, "param mapping not found: "+param.GetKey()), nil
		}
		value, err := dataValueToString(param.GetValue())
		if err != nil {
			return rejectedResponse(cmd, dterrors.CodeCommandInvalid, err.Error()), nil
		}
		if err := c.executeMapping(mapping, value); err != nil {
			_ = c.closeClientLocked()
			return nil, err
		}
		result[param.GetKey()] = value
	}
	c.addMessageOut()
	return successResponse(cmd, result), nil
}

func (c *Connector) handleQuery(ctx context.Context, cmd *dtv1.DeviceMessage, device config.DeviceConfig) (*dtv1.CommandResponsePayload, error) {
	keys := map[string]struct{}{}
	for _, key := range cmd.GetQuery().GetKeys() {
		keys[key] = struct{}{}
	}
	result := make(map[string]string)
	for _, datapoint := range device.Datapoints {
		if len(keys) > 0 {
			if _, ok := keys[datapoint.Key]; !ok {
				continue
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		raw, err := c.readDatapoint(datapoint)
		if err != nil {
			_ = c.closeClientLocked()
			return nil, err
		}
		value, err := decodeValue(raw, datapoint)
		if err != nil {
			return nil, err
		}
		text, err := dataValueToString(value)
		if err != nil {
			return nil, err
		}
		result[datapoint.Key] = text
	}
	c.addMessageOut()
	return successResponse(cmd, result), nil
}

func (c *Connector) executeMapping(mapping config.ActionMapping, rawValue string) error {
	actionType := normalizeActionType(mapping.Type)
	switch actionType {
	case "write_single_coil":
		value, err := strconv.ParseBool(rawValue)
		if err != nil {
			return err
		}
		return c.client.WriteCoil(mapping.Address, value)
	case "write_coils":
		values, err := parseBoolValues(rawValue, mapping.Values)
		if err != nil {
			return err
		}
		return c.client.WriteCoils(mapping.Address, values)
	case "write_single_register":
		values, err := encodeRegisterValues(rawValue, mapping.DataType)
		if err != nil {
			return err
		}
		return c.client.WriteRegister(mapping.Address, values[0])
	case "write_registers":
		values, err := parseRegisterValues(rawValue, mapping)
		if err != nil {
			return err
		}
		return c.client.WriteRegisters(mapping.Address, values)
	default:
		return fmt.Errorf("%s: unsupported action mapping type %q", dterrors.CodeCommandUnsupported, mapping.Type)
	}
}

func (c *Connector) ensureClient() error {
	if c.client == nil {
		client, err := c.clientFactory(c.cfg.Connection)
		if err != nil {
			return err
		}
		c.client = client
	}
	if c.clientOpen {
		return nil
	}
	if err := c.client.Open(); err != nil {
		return err
	}
	c.clientOpen = true
	return nil
}

func (c *Connector) closeClientLocked() error {
	if c.client == nil {
		c.clientOpen = false
		return nil
	}
	err := c.client.Close()
	c.client = nil
	c.clientOpen = false
	return err
}

func (c *Connector) setUnit(device config.DeviceConfig) error {
	unitID := device.UnitID
	if unitID == 0 {
		unitID = c.cfg.Connection.UnitID
	}
	if unitID == 0 {
		unitID = 1
	}
	return c.client.SetUnitId(unitID)
}

func (c *Connector) actionMapping(device config.DeviceConfig, name string) (config.ActionMapping, bool) {
	if device.ActionMappings != nil {
		if mapping, ok := device.ActionMappings[name]; ok {
			return mapping, true
		}
	}
	if c.cfg.ActionMappings != nil {
		mapping, ok := c.cfg.ActionMappings[name]
		return mapping, ok
	}
	return config.ActionMapping{}, false
}

func (c *Connector) deviceConfig(deviceID string) (config.DeviceConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	device, ok := c.deviceConfigs[deviceID]
	return device, ok
}

func (c *Connector) snapshotDeviceConfigs() []config.DeviceConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]config.DeviceConfig, 0, len(c.deviceConfigs))
	for _, device := range c.deviceConfigs {
		out = append(out, device)
	}
	return out
}

func (c *Connector) setState(state, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.State = state
	c.status.ErrorMessage = message
}

func (c *Connector) markError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.State = connector.StateError
	c.status.ErrorMessage = err.Error()
	c.status.Stats.ErrorsTotal++
}

func (c *Connector) addMessageIn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Stats.MessagesIn++
}

func (c *Connector) addMessageOut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Stats.MessagesOut++
}

func (c *Connector) markAllDevices(ctx context.Context, state dtv1.DeviceState, upstream chan<- *dtv1.DeviceMessage) {
	for _, device := range c.snapshotDeviceConfigs() {
		c.markDeviceState(ctx, device.DeviceID, state, upstream)
	}
}

// markDeviceState 在锁内更新设备状态并构造状态消息,锁外做带 ctx 的阻塞发送。
// 不再为发送启动 goroutine:既避免停机时 goroutine 泄漏,也保证状态消息有序。
func (c *Connector) markDeviceState(ctx context.Context, deviceID string, state dtv1.DeviceState, upstream chan<- *dtv1.DeviceMessage) {
	var msg *dtv1.DeviceMessage
	c.mu.Lock()
	for _, device := range c.devices {
		if device.GetIdentity().GetDeviceId() != deviceID {
			continue
		}
		device.LastSeen = time.Now().UnixMilli()
		if device.State != state {
			device.State = state
			if upstream != nil {
				msg = statusMessage(c.cfg, device)
			}
		}
		break
	}
	c.mu.Unlock()
	if msg == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case upstream <- msg:
	case <-ctx.Done():
	}
}

func statusMessage(cfg config.ConnectorConfig, device *dtv1.DeviceInfo) *dtv1.DeviceMessage {
	now := time.Now().UnixMilli()
	return &dtv1.DeviceMessage{
		MessageId: fmt.Sprintf("modbus-status-%s-%d", device.GetIdentity().GetDeviceId(), time.Now().UnixNano()),
		Timestamp: now,
		Direction: dtv1.Direction_UPSTREAM,
		Device:    proto.Clone(device.GetIdentity()).(*dtv1.DeviceIdentity),
		Type:      dtv1.MessageType_STATUS,
		Payload: &dtv1.DeviceMessage_Status{
			Status: &dtv1.StatusPayload{
				State:    device.GetState(),
				Reason:   cfg.Protocol,
				LastSeen: now,
			},
		},
		Metadata: map[string]string{
			"protocol": cfg.Protocol,
		},
	}
}

func mappingValue(mapping config.ActionMapping, params map[string]string) (string, error) {
	if mapping.Param != "" {
		value, ok := params[mapping.Param]
		if !ok {
			return "", fmt.Errorf("missing command param %q", mapping.Param)
		}
		return value, nil
	}
	if mapping.Value != "" {
		return mapping.Value, nil
	}
	if len(params) == 1 {
		for _, value := range params {
			return value, nil
		}
	}
	return "", fmt.Errorf("command mapping requires param or value")
}

func parseBoolValues(raw string, configured []string) ([]bool, error) {
	source := configured
	if len(source) == 0 {
		source = strings.Split(raw, ",")
	}
	values := make([]bool, 0, len(source))
	for _, item := range source {
		value, err := strconv.ParseBool(strings.TrimSpace(item))
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func parseRegisterValues(raw string, mapping config.ActionMapping) ([]uint16, error) {
	if len(mapping.Values) == 0 {
		return encodeRegisterValues(raw, mapping.DataType)
	}
	values := make([]uint16, 0, len(mapping.Values))
	for _, item := range mapping.Values {
		encoded, err := encodeRegisterValues(strings.TrimSpace(item), mapping.DataType)
		if err != nil {
			return nil, err
		}
		values = append(values, encoded...)
	}
	return values, nil
}

func normalizeActionType(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
}

func successResponse(cmd *dtv1.DeviceMessage, result map[string]string) *dtv1.CommandResponsePayload {
	return &dtv1.CommandResponsePayload{
		CommandId: cmd.GetCommandId(),
		Status:    dtv1.CommandStatus_SUCCESS,
		Message:   "success",
		Result:    result,
	}
}

func rejectedResponse(cmd *dtv1.DeviceMessage, code, message string) *dtv1.CommandResponsePayload {
	return &dtv1.CommandResponsePayload{
		CommandId: cmd.GetCommandId(),
		Status:    dtv1.CommandStatus_REJECTED,
		Message:   fmt.Sprintf("%s: %s", code, message),
	}
}

func nativeClientFactory(connection config.ConnectionConfig) (Client, error) {
	url := connection.URL
	if url == "" {
		host := connection.Host
		if host == "" {
			host = "127.0.0.1"
		}
		port := connection.Port
		if port == 0 {
			port = 502
		}
		url = fmt.Sprintf("tcp://%s:%d", host, port)
	}
	timeout := time.Duration(connection.TimeoutMillis) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Second
	}
	return smodbus.NewClient(&smodbus.ClientConfiguration{
		URL:     url,
		Timeout: timeout,
		Logger:  log.New(io.Discard, "", 0),
	})
}

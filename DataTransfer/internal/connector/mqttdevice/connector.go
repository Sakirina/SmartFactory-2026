// Package mqttdevice 实现 MQTT Device Connector(接口文档 4.2.3):
// 订阅设备侧 JSON 遥测/状态/事件/回执 topic(模板可自定义,按段匹配提取 device_id),
// 下行指令编码为 JSON 发布到设备 command topic。
package mqttdevice

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	dterrors "competition2026/product/datatransfer/internal/errors"
	"competition2026/product/datatransfer/internal/security"
	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/proto"
)

const Protocol = "mqtt_device"

const (
	kindTelemetry       = "telemetry"
	kindStatus          = "status"
	kindEvent           = "event"
	kindCommandResponse = "cmd-response"
)

// topicRoute 描述一类设备上行消息的 topic 模板,模板中 {device_id} 为设备占位符。
type topicRoute struct {
	kind     string
	template string
}

type Connector struct {
	mu            sync.RWMutex
	cfg           config.ConnectorConfig
	client        paho.Client
	status        connector.Status
	devices       []*dtv1.DeviceInfo
	deviceConfigs map[string]config.DeviceConfig
	startedAt     time.Time
	upstream      chan<- *dtv1.DeviceMessage
	subscribed    []string
}

func init() {
	connector.Register(Protocol, func() connector.Connector {
		return NewConnector()
	})
}

func NewConnector() *Connector {
	return &Connector{}
}

func (c *Connector) Init(cfg config.ConnectorConfig) error {
	if strings.ToLower(cfg.Protocol) != Protocol {
		return fmt.Errorf("%s: unsupported protocol %q", dterrors.CodeConnectorInvalid, cfg.Protocol)
	}
	if cfg.Connection.URL == "" {
		return fmt.Errorf("%s: mqtt_device connection.url is required", dterrors.CodeConnectorInvalid)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = cfg
	c.status = connector.NewStatus(cfg.ConnectorID, cfg.Protocol)
	c.status.DeviceCount = len(cfg.Devices)
	c.devices = make([]*dtv1.DeviceInfo, 0, len(cfg.Devices))
	c.deviceConfigs = make(map[string]config.DeviceConfig, len(cfg.Devices))
	now := time.Now()
	for _, device := range cfg.Devices {
		c.deviceConfigs[device.DeviceID] = device
		c.devices = append(c.devices, connector.DeviceInfoFromConfig(cfg.ConnectorID, cfg.Protocol, mergeTags(cfg.DefaultTags, device.Tags), device, dtv1.DeviceState_OFFLINE, now))
	}
	return nil
}

func (c *Connector) Start(ctx context.Context, upstream chan<- *dtv1.DeviceMessage) error {
	c.mu.Lock()
	c.startedAt = time.Now()
	c.upstream = upstream
	c.mu.Unlock()
	if err := c.connect(); err != nil {
		c.setState(connector.StateError, err.Error())
		return err
	}
	c.setState(connector.StateRunning, "")
	<-ctx.Done()
	return c.Stop()
}

func (c *Connector) SendCommand(ctx context.Context, cmd *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
	deviceID := cmd.GetDevice().GetDeviceId()
	c.mu.RLock()
	client := c.client
	device, ok := c.deviceConfigs[deviceID]
	cfg := c.cfg
	c.mu.RUnlock()
	if !ok {
		return rejectedResponse(cmd, dterrors.CodeCommandNoRoute, "device is not managed by this connector"), nil
	}
	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("%s: mqtt device client is not connected", dterrors.CodeConnectorConnectFailed)
	}
	payload, err := commandPayload(cmd)
	if err != nil {
		return rejectedResponse(cmd, dterrors.CodeCommandInvalid, err.Error()), nil
	}
	topic := commandTopic(cfg, device, cmd)
	token := client.Publish(topic, 1, false, payload)
	if err := waitToken(ctx, token); err != nil {
		return nil, err
	}
	c.addMessageOut()
	return &dtv1.CommandResponsePayload{
		CommandId: cmd.GetCommandId(),
		Status:    dtv1.CommandStatus_SUCCESS,
		Message:   "command published to mqtt device",
		Result:    map[string]string{"topic": topic},
	}, nil
}

func (c *Connector) Stop() error {
	c.mu.Lock()
	client := c.client
	c.client = nil
	c.setStateLocked(connector.StateStopped, "")
	c.mu.Unlock()
	// Disconnect 可能阻塞至 250ms,放在锁外执行以免阻塞 Status()/Devices()。
	if client != nil && client.IsConnected() {
		client.Disconnect(250)
	}
	return nil
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

func (c *Connector) ReloadConfig(cfg config.ConnectorConfig) error {
	if err := c.Init(cfg); err != nil {
		return err
	}
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	if client != nil && client.IsConnected() {
		return c.subscribe(client)
	}
	return nil
}

func (c *Connector) connect() error {
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	opts := paho.NewClientOptions().
		AddBroker(cfg.Connection.URL).
		SetClientID(clientID(cfg)).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectTimeout(time.Duration(timeoutMillis(cfg.Connection.TimeoutMillis)) * time.Millisecond).
		SetOrderMatters(false)
	if cfg.Connection.Username != "" {
		opts.SetUsername(cfg.Connection.Username)
	}
	if cfg.Connection.Password != "" {
		opts.SetPassword(cfg.Connection.Password)
	}
	if cfg.Connection.TLS.Enabled {
		tlsCfg, err := security.TLSConfig(cfg.Connection.TLS)
		if err != nil {
			return err
		}
		opts.SetTLSConfig(tlsCfg)
	}
	opts.SetConnectionLostHandler(func(_ paho.Client, err error) {
		c.setState(connector.StateError, err.Error())
	})
	opts.SetOnConnectHandler(func(client paho.Client) {
		if err := c.subscribe(client); err != nil {
			slog.Warn("mqtt device subscribe failed", "connector_id", cfg.ConnectorID, "error", err)
			c.setState(connector.StateError, err.Error())
			return
		}
		c.setState(connector.StateRunning, "")
	})
	client := paho.NewClient(opts)
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()
	token := client.Connect()
	if !token.WaitTimeout(time.Duration(timeoutMillis(cfg.Connection.TimeoutMillis)) * time.Millisecond) {
		return fmt.Errorf("%s: mqtt device connect timeout", dterrors.CodeConnectorConnectFailed)
	}
	return token.Error()
}

// topicRoutes 返回四类上行消息的 topic 模板(可被连接配置覆盖,默认 devices/{device_id}/<kind>)。
func topicRoutes(cfg config.ConnectorConfig) []topicRoute {
	return []topicRoute{
		{kindTelemetry, templateOrDefault(cfg.Connection.TelemetryTopic, "devices/{device_id}/telemetry")},
		{kindStatus, templateOrDefault(cfg.Connection.StatusTopic, "devices/{device_id}/status")},
		{kindEvent, templateOrDefault(cfg.Connection.EventTopic, "devices/{device_id}/event")},
		{kindCommandResponse, templateOrDefault(cfg.Connection.CommandResponseTopic, "devices/{device_id}/cmd-response")},
	}
}

func (c *Connector) subscribe(client paho.Client) error {
	c.mu.RLock()
	cfg := c.cfg
	previous := append([]string(nil), c.subscribed...)
	c.mu.RUnlock()

	filters := make(map[string]byte, 4)
	for _, route := range topicRoutes(cfg) {
		filters[subscriptionFilter(route.template)] = 1
	}
	token := client.SubscribeMultiple(filters, c.routeMessage)
	token.Wait()
	if err := token.Error(); err != nil {
		return err
	}

	// 退订不再使用的旧 topic,避免热加载后残留订阅继续投递消息。
	var stale []string
	for _, topic := range previous {
		if _, ok := filters[topic]; !ok {
			stale = append(stale, topic)
		}
	}
	if len(stale) > 0 {
		unsubToken := client.Unsubscribe(stale...)
		unsubToken.Wait()
		if err := unsubToken.Error(); err != nil {
			slog.Warn("mqtt device unsubscribe stale topics failed", "connector_id", cfg.ConnectorID, "error", err)
		}
	}

	current := make([]string, 0, len(filters))
	for topic := range filters {
		current = append(current, topic)
	}
	c.mu.Lock()
	c.subscribed = current
	c.mu.Unlock()
	return nil
}

func (c *Connector) routeMessage(_ paho.Client, msg paho.Message) {
	c.mu.RLock()
	cfg := c.cfg
	upstream := c.upstream
	c.mu.RUnlock()
	if upstream == nil {
		return
	}
	kind, deviceID := matchRoute(topicRoutes(cfg), msg.Topic())
	if kind == "" || deviceID == "" {
		return
	}
	c.mu.RLock()
	device, ok := c.deviceConfigs[deviceID]
	c.mu.RUnlock()
	if !ok {
		return
	}
	var out *dtv1.DeviceMessage
	var err error
	switch kind {
	case kindTelemetry:
		out, err = c.telemetryMessage(cfg, device, msg.Payload())
	case kindStatus:
		out = c.statusMessage(cfg, device, msg.Payload())
	case kindEvent:
		out = c.eventMessage(cfg, device, msg.Payload())
	case kindCommandResponse:
		out = c.commandResponseMessage(cfg, device, msg.Payload())
	}
	if err != nil {
		c.markError(err)
		return
	}
	if out == nil {
		return
	}
	c.markDevice(device.DeviceID, dtv1.DeviceState_ONLINE)
	c.addMessageIn()
	select {
	case upstream <- out:
	default:
		// 通道已满即背压:丢弃并计数,不阻塞 MQTT 回调线程(FR-S-039 通过指标可见)。
		c.markError(fmt.Errorf("%s: upstream channel is full, message dropped", dterrors.CodeBackpressureOn))
	}
}

func (c *Connector) telemetryMessage(cfg config.ConnectorConfig, device config.DeviceConfig, payload []byte) (*dtv1.DeviceMessage, error) {
	datapoints := make([]*dtv1.Datapoint, 0, len(device.Datapoints))
	for _, dp := range device.Datapoints {
		result := gjson.GetBytes(payload, dp.Source)
		if !result.Exists() {
			continue
		}
		value, err := jsonValue(result, dp)
		if err != nil {
			return nil, err
		}
		datapoints = append(datapoints, &dtv1.Datapoint{Key: dp.Key, Value: value, Timestamp: time.Now().UnixMilli(), Quality: qualityFromString(dp.Quality), Unit: dp.Unit})
	}
	if len(datapoints) == 0 {
		return nil, nil
	}
	return message(cfg, device, dtv1.MessageType_TELEMETRY, &dtv1.DeviceMessage_Telemetry{Telemetry: &dtv1.TelemetryPayload{Datapoints: datapoints}}), nil
}

func (c *Connector) statusMessage(cfg config.ConnectorConfig, device config.DeviceConfig, payload []byte) *dtv1.DeviceMessage {
	state := stateFromString(gjson.GetBytes(payload, "state").String())
	if state == dtv1.DeviceState_STATE_UNSPECIFIED {
		state = dtv1.DeviceState_UNKNOWN
	}
	return message(cfg, device, dtv1.MessageType_STATUS, &dtv1.DeviceMessage_Status{Status: &dtv1.StatusPayload{
		State:    state,
		Reason:   gjson.GetBytes(payload, "reason").String(),
		LastSeen: time.Now().UnixMilli(),
	}})
}

func (c *Connector) eventMessage(cfg config.ConnectorConfig, device config.DeviceConfig, payload []byte) *dtv1.DeviceMessage {
	return message(cfg, device, dtv1.MessageType_EVENT, &dtv1.DeviceMessage_Event{Event: &dtv1.EventPayload{
		EventType:   gjson.GetBytes(payload, "type").String(),
		Severity:    severityFromString(gjson.GetBytes(payload, "severity").String()),
		Description: gjson.GetBytes(payload, "description").String(),
		Data:        jsonObjectMap(payload, "data"),
	}})
}

func (c *Connector) commandResponseMessage(cfg config.ConnectorConfig, device config.DeviceConfig, payload []byte) *dtv1.DeviceMessage {
	commandID := gjson.GetBytes(payload, "command_id").String()
	return &dtv1.DeviceMessage{
		MessageId: fmt.Sprintf("mqtt-device-response-%s-%d", device.DeviceID, time.Now().UnixNano()),
		Timestamp: time.Now().UnixMilli(),
		Direction: dtv1.Direction_UPSTREAM,
		Device:    identity(cfg, device),
		Type:      dtv1.MessageType_CMD_RESPONSE,
		CommandId: commandID,
		Payload: &dtv1.DeviceMessage_CmdResponse{CmdResponse: &dtv1.CommandResponsePayload{
			CommandId: commandID,
			Status:    statusFromString(gjson.GetBytes(payload, "status").String()),
			Message:   gjson.GetBytes(payload, "message").String(),
			Result:    jsonObjectMap(payload, "result"),
		}},
		Metadata: map[string]string{"protocol": Protocol},
	}
}

func message(cfg config.ConnectorConfig, device config.DeviceConfig, typ dtv1.MessageType, payload any) *dtv1.DeviceMessage {
	msg := &dtv1.DeviceMessage{
		MessageId: fmt.Sprintf("mqtt-device-%s-%d", device.DeviceID, time.Now().UnixNano()),
		Timestamp: time.Now().UnixMilli(),
		Direction: dtv1.Direction_UPSTREAM,
		Device:    identity(cfg, device),
		Type:      typ,
		Metadata:  map[string]string{"protocol": Protocol},
	}
	switch typed := payload.(type) {
	case *dtv1.DeviceMessage_Telemetry:
		msg.Payload = typed
	case *dtv1.DeviceMessage_Status:
		msg.Payload = typed
	case *dtv1.DeviceMessage_Event:
		msg.Payload = typed
	}
	return msg
}

func commandPayload(cmd *dtv1.DeviceMessage) ([]byte, error) {
	switch cmd.GetType() {
	case dtv1.MessageType_CONTROL:
		return json.Marshal(map[string]any{"command_id": cmd.GetCommandId(), "type": "control", "action": cmd.GetControl().GetAction(), "params": cmd.GetControl().GetParams()})
	case dtv1.MessageType_PARAM_UPDATE:
		params := map[string]any{}
		for _, param := range cmd.GetParamUpdate().GetParams() {
			params[param.GetKey()] = dataValueToAny(param.GetValue())
		}
		return json.Marshal(map[string]any{"command_id": cmd.GetCommandId(), "type": "param_update", "params": params})
	default:
		return nil, fmt.Errorf("unsupported command type %s", cmd.GetType().String())
	}
}

func commandTopic(cfg config.ConnectorConfig, device config.DeviceConfig, cmd *dtv1.DeviceMessage) string {
	if cmd.GetType() == dtv1.MessageType_CONTROL {
		if mapping, ok := actionMapping(cfg, device, cmd.GetControl().GetAction()); ok && mapping.Topic != "" {
			return renderTopic(mapping.Topic, device.DeviceID)
		}
	}
	if cfg.Connection.CommandTopic != "" {
		return renderTopic(cfg.Connection.CommandTopic, device.DeviceID)
	}
	return "devices/" + device.DeviceID + "/command"
}

func actionMapping(cfg config.ConnectorConfig, device config.DeviceConfig, action string) (config.ActionMapping, bool) {
	if mapping, ok := device.ActionMappings[action]; ok {
		return mapping, true
	}
	mapping, ok := cfg.ActionMappings[action]
	return mapping, ok
}

func jsonValue(result gjson.Result, dp config.DatapointConfig) (*dtv1.DataValue, error) {
	switch strings.ToLower(dp.DataType) {
	case "bool":
		return &dtv1.DataValue{Kind: &dtv1.DataValue_BoolValue{BoolValue: result.Bool()}}, nil
	case "int", "int16", "int32", "int64", "uint16", "uint32":
		return numericValue(float64(result.Int()), dp, true), nil
	case "float", "float32", "float64", "double":
		return numericValue(result.Float(), dp, false), nil
	case "string":
		return &dtv1.DataValue{Kind: &dtv1.DataValue_StringValue{StringValue: result.String()}}, nil
	default:
		if result.Type == gjson.Number {
			return numericValue(result.Float(), dp, false), nil
		}
		return &dtv1.DataValue{Kind: &dtv1.DataValue_StringValue{StringValue: result.String()}}, nil
	}
}

func numericValue(raw float64, dp config.DatapointConfig, integral bool) *dtv1.DataValue {
	scale := 1.0
	if dp.Scale != nil {
		scale = *dp.Scale
	}
	value := raw*scale + dp.Offset
	if integral && scale == 1 && dp.Offset == 0 {
		return &dtv1.DataValue{Kind: &dtv1.DataValue_IntValue{IntValue: int64(value)}}
	}
	return &dtv1.DataValue{Kind: &dtv1.DataValue_DoubleValue{DoubleValue: value}}
}

func identity(cfg config.ConnectorConfig, device config.DeviceConfig) *dtv1.DeviceIdentity {
	return &dtv1.DeviceIdentity{
		DeviceId: device.DeviceID, DeviceName: device.DeviceName, DeviceType: device.DeviceType,
		ConnectorId: cfg.ConnectorID, Protocol: cfg.Protocol, Tags: mergeTags(cfg.DefaultTags, device.Tags),
	}
}

func templateOrDefault(configured, fallback string) string {
	if configured == "" {
		return fallback
	}
	return configured
}

// subscriptionFilter 把模板中的 {device_id} 占位符转换为 MQTT 单层通配符。
func subscriptionFilter(template string) string {
	return strings.ReplaceAll(template, "{device_id}", "+")
}

func renderTopic(template, deviceID string) string {
	return strings.ReplaceAll(template, "{device_id}", deviceID)
}

// matchRoute 将实际 topic 与各模板逐段匹配,返回消息类别与提取出的 device_id。
// 支持自定义 topic 模板(不再假定固定的 devices/ 前缀和后缀)。
func matchRoute(routes []topicRoute, topic string) (string, string) {
	topicParts := strings.Split(topic, "/")
	for _, route := range routes {
		templateParts := strings.Split(route.template, "/")
		if len(templateParts) != len(topicParts) {
			continue
		}
		deviceID := ""
		matched := true
		for idx, part := range templateParts {
			switch part {
			case "{device_id}", "+":
				deviceID = topicParts[idx]
			default:
				if part != topicParts[idx] {
					matched = false
				}
			}
			if !matched {
				break
			}
		}
		if matched && deviceID != "" {
			return route.kind, deviceID
		}
	}
	return "", ""
}

func clientID(cfg config.ConnectorConfig) string {
	if cfg.Connection.Host != "" {
		return cfg.Connection.Host
	}
	return "dt-" + cfg.ConnectorID
}

func timeoutMillis(value int) int {
	if value <= 0 {
		return 1000
	}
	return value
}

func waitToken(ctx context.Context, token paho.Token) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-token.Done():
		return token.Error()
	}
}

func rejectedResponse(cmd *dtv1.DeviceMessage, code, message string) *dtv1.CommandResponsePayload {
	return &dtv1.CommandResponsePayload{CommandId: cmd.GetCommandId(), Status: dtv1.CommandStatus_REJECTED, Message: fmt.Sprintf("%s: %s", code, message)}
}

func dataValueToAny(value *dtv1.DataValue) any {
	switch typed := value.GetKind().(type) {
	case *dtv1.DataValue_BoolValue:
		return typed.BoolValue
	case *dtv1.DataValue_DoubleValue:
		return typed.DoubleValue
	case *dtv1.DataValue_IntValue:
		return typed.IntValue
	case *dtv1.DataValue_StringValue:
		return typed.StringValue
	default:
		return nil
	}
}

func jsonObjectMap(payload []byte, path string) map[string]string {
	result := gjson.GetBytes(payload, path)
	if !result.IsObject() {
		return nil
	}
	out := map[string]string{}
	result.ForEach(func(key, value gjson.Result) bool {
		out[key.String()] = value.String()
		return true
	})
	return out
}

func mergeTags(base, override map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		out[key] = value
	}
	return out
}

func qualityFromString(value string) dtv1.DataQuality {
	switch strings.ToLower(value) {
	case "bad":
		return dtv1.DataQuality_BAD
	case "uncertain":
		return dtv1.DataQuality_UNCERTAIN
	default:
		return dtv1.DataQuality_GOOD
	}
}

func stateFromString(value string) dtv1.DeviceState {
	switch strings.ToLower(value) {
	case "online":
		return dtv1.DeviceState_ONLINE
	case "offline":
		return dtv1.DeviceState_OFFLINE
	case "error":
		return dtv1.DeviceState_ERROR
	case "unknown":
		return dtv1.DeviceState_UNKNOWN
	default:
		return dtv1.DeviceState_STATE_UNSPECIFIED
	}
}

func severityFromString(value string) dtv1.Severity {
	switch strings.ToLower(value) {
	case "warning":
		return dtv1.Severity_WARNING
	case "alarm":
		return dtv1.Severity_ALARM
	case "critical":
		return dtv1.Severity_CRITICAL
	default:
		return dtv1.Severity_INFO
	}
}

func statusFromString(value string) dtv1.CommandStatus {
	switch strings.ToLower(value) {
	case "success":
		return dtv1.CommandStatus_SUCCESS
	case "timeout":
		return dtv1.CommandStatus_TIMEOUT
	case "rejected":
		return dtv1.CommandStatus_REJECTED
	default:
		return dtv1.CommandStatus_FAILURE
	}
}

func (c *Connector) markDevice(deviceID string, state dtv1.DeviceState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for idx := range c.devices {
		if c.devices[idx].GetIdentity().GetDeviceId() == deviceID {
			c.devices[idx].State = state
			c.devices[idx].LastSeen = time.Now().UnixMilli()
		}
	}
}

func (c *Connector) setState(state string, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setStateLocked(state, message)
}

func (c *Connector) setStateLocked(state string, message string) {
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

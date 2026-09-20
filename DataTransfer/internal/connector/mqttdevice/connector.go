// Package mqttdevice 实现 MQTT Device Connector(接口文档 4.2.3):
// 订阅设备侧 JSON 遥测/状态/事件/回执 topic(模板可自定义,按段匹配提取 device_id),
// 下行指令编码为 JSON 发布到设备 command topic。
package mqttdevice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	"competition2026/product/datatransfer/internal/conversion"
	dterrors "competition2026/product/datatransfer/internal/errors"
	"competition2026/product/datatransfer/internal/security"
	"github.com/eclipse/paho.golang/autopaho"
	paho5 "github.com/eclipse/paho.golang/paho"
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
	mu               sync.RWMutex
	cfg              config.ConnectorConfig
	client           paho.Client
	status           connector.Status
	devices          []*dtv1.DeviceInfo
	deviceConfigs    map[string]config.DeviceConfig
	startedAt        time.Time
	upstream         chan<- *dtv1.DeviceMessage
	subscribed       []string
	v5               *autopaho.ConnectionManager
	ctx              context.Context
	cancel           context.CancelFunc
	pending          map[string]pendingCommand
	acknowledgements chan *dtv1.DeviceMessage
}

type pendingCommand struct {
	device   string
	response chan *dtv1.CommandResponsePayload
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
	if cfg.Connection.MQTTVersion != "" && cfg.Connection.MQTTVersion != "3.1.1" && cfg.Connection.MQTTVersion != "5.0" {
		return errors.New("mqtt_version must be 3.1.1 or 5.0")
	}
	if cfg.Connection.URL == "" {
		return fmt.Errorf("%s: mqtt_device connection.url is required", dterrors.CodeConnectorInvalid)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = cfg
	c.pending = make(map[string]pendingCommand)
	c.acknowledgements = make(chan *dtv1.DeviceMessage, 256)
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

func (c *Connector) Start(parent context.Context, upstream chan<- *dtv1.DeviceMessage) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer c.Stop()
	c.mu.Lock()
	c.startedAt = time.Now()
	c.upstream = upstream
	c.ctx, c.cancel = ctx, cancel
	c.mu.Unlock()
	if err := c.connectProtocol(ctx); err != nil {
		c.setState(connector.StateError, err.Error())
		return err
	}
	c.setState(connector.StateRunning, "")
	done := make(chan struct{})
	go func() { defer close(done); c.publishAcknowledgements(ctx) }()
	<-ctx.Done()
	<-done
	return c.Stop()
}

func (c *Connector) SendCommand(ctx context.Context, cmd *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
	deviceID := cmd.GetDevice().GetDeviceId()
	c.mu.RLock()
	client, v5 := c.client, c.v5
	device, ok := c.deviceConfigs[deviceID]
	cfg := c.cfg
	c.mu.RUnlock()
	if !ok {
		return rejectedResponse(cmd, dterrors.CodeCommandNoRoute, "device is not managed by this connector"), nil
	}
	if v5 == nil && (client == nil || !client.IsConnected()) {
		return nil, errors.New("mqtt device client is not connected")
	}
	payload, err := commandPayload(cmd)
	if err != nil {
		return rejectedResponse(cmd, dterrors.CodeCommandInvalid, err.Error()), nil
	}
	timeout := 5 * time.Second
	if ms := cmd.GetControl().GetOptions().GetTimeoutMs(); ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pending := pendingCommand{device: deviceID, response: make(chan *dtv1.CommandResponsePayload, 1)}
	c.mu.Lock()
	if _, exists := c.pending[cmd.GetCommandId()]; exists {
		c.mu.Unlock()
		return nil, errors.New("command is already in flight")
	}
	c.pending[cmd.GetCommandId()] = pending
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, cmd.GetCommandId()); c.mu.Unlock() }()
	topic := commandTopic(cfg, device, cmd)
	if v5 != nil {
		_, err = v5.Publish(ctx, &paho5.Publish{Topic: topic, QoS: 1, Payload: payload})
	} else {
		err = waitToken(ctx, client.Publish(topic, 1, false, payload))
	}
	if err != nil {
		return nil, err
	}
	c.addMessageOut()
	select {
	case result := <-pending.response:
		return result, nil
	case <-ctx.Done():
		return &dtv1.CommandResponsePayload{CommandId: cmd.GetCommandId(), Status: dtv1.CommandStatus_RESULT_UNKNOWN, Message: "device business acknowledgement was not received"}, nil
	}
}

func (c *Connector) Stop() error {
	c.mu.Lock()
	client := c.client
	v5, cancel := c.v5, c.cancel
	c.v5, c.cancel = nil, nil
	c.client = nil
	c.setStateLocked(connector.StateStopped, "")
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if v5 != nil {
		ctx, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = v5.Disconnect(ctx)
	}
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
	ready := make(chan error, 1)
	opts.SetOnConnectHandler(func(client paho.Client) {
		if err := c.subscribe(client); err != nil {
			slog.Warn("mqtt device subscribe failed", "connector_id", cfg.ConnectorID, "error", err)
			c.setState(connector.StateError, err.Error())
			select {
			case ready <- err:
			default:
			}
			return
		}
		c.setState(connector.StateRunning, "")
		select {
		case ready <- nil:
		default:
		}
	})
	client := paho.NewClient(opts)
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()
	token := client.Connect()
	if !token.WaitTimeout(time.Duration(timeoutMillis(cfg.Connection.TimeoutMillis)) * time.Millisecond) {
		return fmt.Errorf("%s: mqtt device connect timeout", dterrors.CodeConnectorConnectFailed)
	}
	if err := token.Error(); err != nil {
		return err
	}
	select {
	case err := <-ready:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("mqtt subscriptions timed out")
	}
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
	c.routePayload(msg.Topic(), msg.Payload())
}
func (c *Connector) routePayload(topic string, payload []byte) {
	if !json.Valid(payload) || len(payload) > 1<<20 {
		c.markError(errors.New("invalid MQTT JSON payload"))
		return
	}
	c.mu.RLock()
	cfg := c.cfg
	upstream := c.upstream
	ctx := c.ctx
	c.mu.RUnlock()
	if upstream == nil {
		return
	}
	kind, deviceID := matchRoute(topicRoutes(cfg), topic)
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
		out, err = c.telemetryMessage(cfg, device, payload)
	case kindStatus:
		out = c.statusMessage(cfg, device, payload)
	case kindEvent:
		out = c.eventMessage(cfg, device, payload)
	case kindCommandResponse:
		out = c.commandResponseMessage(cfg, device, payload)
	}
	if err != nil {
		c.markError(err)
		return
	}
	if out == nil {
		return
	}
	sourceID := gjson.GetBytes(payload, "message_id").String()
	if out.Metadata == nil {
		out.Metadata = map[string]string{}
	}
	out.Metadata["mqtt_message_id"] = sourceID
	if sourceID != "" {
		out.MessageId = cfg.ConnectorID + ":" + device.DeviceID + ":" + sourceID
		out.Timestamp = gjson.GetBytes(payload, "timestamp").Int()
		if out.Timestamp <= 0 {
			c.markError(errors.New("identified messages require timestamp"))
			return
		}
		if out.GetStatus() != nil {
			out.GetStatus().LastSeen = out.Timestamp
		}
	}
	out.SourceSequence = gjson.GetBytes(payload, "source_sequence").Uint()
	out.TimeSource = "collector"
	if out.Timestamp > 0 && gjson.GetBytes(payload, "timestamp").Exists() {
		out.TimeSource = "device"
	}
	state := dtv1.DeviceState_ONLINE
	if out.GetStatus() != nil {
		state = out.GetStatus().GetState()
	}
	c.markDevice(device.DeviceID, state)
	if response := out.GetCmdResponse(); response != nil {
		c.mu.RLock()
		pending, found := c.pending[response.CommandId]
		c.mu.RUnlock()
		if found && pending.device == device.DeviceID {
			select {
			case pending.response <- proto.Clone(response).(*dtv1.CommandResponsePayload):
			default:
			}
		}
	}
	c.addMessageIn()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case upstream <- out:
	case <-ctx.Done():
	}
}

func (c *Connector) telemetryMessage(cfg config.ConnectorConfig, device config.DeviceConfig, payload []byte) (*dtv1.DeviceMessage, error) {
	timestamp := gjson.GetBytes(payload, "timestamp").Int()
	source := "device"
	if timestamp <= 0 {
		timestamp = time.Now().UnixMilli()
		source = "collector"
	}
	if gjson.GetBytes(payload, "message_id").String() != "" && source == "collector" {
		return nil, errors.New("identified telemetry requires a device timestamp")
	}
	datapoints := make([]*dtv1.Datapoint, 0, len(device.Datapoints))
	for _, dp := range device.Datapoints {
		result := gjson.GetBytes(payload, dp.Source)
		point := &dtv1.Datapoint{Key: dp.Key, Timestamp: timestamp, TimeSource: source, Quality: qualityFromString(dp.Quality), Unit: dp.Unit}
		if !result.Exists() {
			point.Quality = dtv1.DataQuality_BAD
			point.QualityReason = "configured source field is missing"
		} else {
			value, err := jsonValue(result, dp)
			if err != nil {
				point.Quality = dtv1.DataQuality_BAD
				point.QualityReason = err.Error()
			} else {
				point.Value = value
			}
		}
		quality := gjson.GetBytes(payload, "quality."+dp.Key)
		if quality.Exists() {
			point.Quality = qualityFromString(quality.String())
			point.QualityReason = gjson.GetBytes(payload, "quality_reason."+dp.Key).String()
		}
		datapoints = append(datapoints, point)
	}
	if len(datapoints) == 0 {
		return nil, nil
	}
	out := message(cfg, device, dtv1.MessageType_TELEMETRY, &dtv1.DeviceMessage_Telemetry{Telemetry: &dtv1.TelemetryPayload{Datapoints: datapoints}})
	out.Timestamp = timestamp
	out.TimeSource = source
	return out, nil
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
		return json.Marshal(map[string]any{"command_id": cmd.GetCommandId(), "type": "control", "action": cmd.GetControl().GetAction(), "params": cmd.GetControl().GetParams(), "start_deadline_ms": cmd.GetControl().GetOptions().GetStartDeadlineMs(), "idempotent": cmd.GetControl().GetOptions().GetIdempotent()})
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
	var raw any
	switch result.Type {
	case gjson.Number:
		raw = json.Number(result.Raw)
	case gjson.True:
		raw = true
	case gjson.False:
		raw = false
	case gjson.String:
		raw = result.Str
	default:
		return nil, errors.New("scalar JSON value required")
	}
	return conversion.Value(raw, dp)
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
	case *dtv1.DataValue_UintValue:
		return typed.UintValue
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

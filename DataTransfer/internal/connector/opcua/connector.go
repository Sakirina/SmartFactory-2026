// Package opcua 实现 OPC-UA Connector(Read/Write/Call/Subscribe 契约)。
package opcua

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	"competition2026/product/datatransfer/internal/conversion"
	dterrors "competition2026/product/datatransfer/internal/errors"
	"google.golang.org/protobuf/proto"
)

const Protocol = "opcua"

type Client interface {
	Connect(ctx context.Context) error
	Close(ctx context.Context) error
	Read(ctx context.Context, nodeID string) (any, error)
	Write(ctx context.Context, nodeID string, value any) error
	Call(ctx context.Context, objectID string, methodID string, args []any) ([]any, error)
	Subscribe(ctx context.Context, nodes []string, emit func(nodeID string, value any)) error
}

type ClientFactory func(config.ConnectorConfig) (Client, error)

type Connector struct {
	clientFactory ClientFactory

	mu            sync.RWMutex
	cfg           config.ConnectorConfig
	client        Client
	status        connector.Status
	devices       []*dtv1.DeviceInfo
	deviceConfigs map[string]config.DeviceConfig
	startedAt     time.Time
	cancel        context.CancelFunc
	done          chan struct{}
	factor        atomic.Int64
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
	if cfg.Connection.URL == "" {
		return fmt.Errorf("%s: opcua connection.url is required", dterrors.CodeConnectorInvalid)
	}
	if c.clientFactory == nil {
		c.clientFactory = nativeClientFactory
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

func (c *Connector) Start(parent context.Context, upstream chan<- *dtv1.DeviceMessage) error {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	c.mu.Lock()
	c.cancel, c.done = cancel, done
	c.mu.Unlock()
	defer close(done)
	defer cancel()
	client, err := c.clientFactory(c.snapshotConfig())
	if err != nil {
		c.markError(err)
		return err
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer closeCancel()
		_ = client.Close(closeCtx)
		c.mu.Lock()
		c.client = nil
		c.mu.Unlock()
	}()
	if err := client.Connect(ctx); err != nil {
		c.markError(err)
		c.failedTelemetry(ctx, err, upstream)
		return err
	}
	c.mu.Lock()
	c.client = client
	c.startedAt = time.Now()
	c.mu.Unlock()
	c.setState(connector.StateRunning, "")
	subErr := make(chan error, 1)
	nodes := c.subscriptionNodes()
	pollMode := c.snapshotConfig().Polling.Mode == "poll"
	if len(nodes) > 0 && !pollMode {
		go func() {
			subErr <- client.Subscribe(ctx, nodes, func(nodeID string, value any) {
				if msg, buildErr := c.messageForNode(nodeID, value); buildErr == nil && msg != nil {
					select {
					case upstream <- msg:
						c.markDevice(msg.GetDevice().GetDeviceId(), dtv1.DeviceState_ONLINE)
						c.addMessageIn()
					case <-ctx.Done():
					}
				}
			})
		}()
		defer func() { cancel(); <-subErr }()
	}
	interval := time.Duration(c.snapshotConfig().Polling.IntervalMillis) * time.Millisecond
	var ticks <-chan time.Time
	if pollMode && interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	ticksSeen := int64(0)
	for {
		select {
		case <-ctx.Done():
			c.setState(connector.StateStopped, "")
			return nil
		case err := <-subErr:
			// Return the subscription failure to Manager so it recreates the session.
			subErr <- err
			if ctx.Err() != nil {
				return nil
			}
			if err == nil {
				err = fmt.Errorf("opcua subscription ended unexpectedly")
			}
			c.markError(err)
			c.failedTelemetry(ctx, err, upstream)
			return err
		case <-ticks:
			ticksSeen++
			factor := c.factor.Load()
			if factor > 1 && ticksSeen%factor != 0 {
				continue
			}
			c.poll(ctx, upstream)
		}
	}
}
func (c *Connector) SetCollectionFactor(factor int64) error {
	if factor < 1 || factor > 100 {
		return fmt.Errorf("collection factor outside 1..100")
	}
	c.factor.Store(factor)
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	if v, ok := client.(interface{ SetCollectionFactor(int64) error }); ok {
		return v.SetCollectionFactor(factor)
	}
	return nil
}

func (c *Connector) SendCommand(ctx context.Context, cmd *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
	c.mu.RLock()
	client := c.client
	device, ok := c.deviceConfigs[cmd.GetDevice().GetDeviceId()]
	c.mu.RUnlock()
	if !ok {
		return rejectedResponse(cmd, dterrors.CodeCommandNoRoute, "device is not managed by this connector"), nil
	}
	if client == nil {
		return nil, fmt.Errorf("%s: opcua client is not connected", dterrors.CodeConnectorConnectFailed)
	}
	switch cmd.GetType() {
	case dtv1.MessageType_QUERY:
		result := map[string]string{}
		keys := map[string]struct{}{}
		for _, key := range cmd.GetQuery().GetKeys() {
			keys[key] = struct{}{}
		}
		for _, dp := range device.Datapoints {
			if len(keys) > 0 {
				if _, ok := keys[dp.Key]; !ok {
					continue
				}
			}
			value, err := client.Read(ctx, dp.NodeID)
			if err != nil {
				return nil, err
			}
			if sample, ok := value.(Observation); ok {
				if sample.Quality != dtv1.DataQuality_GOOD {
					return nil, fmt.Errorf("opcua query %s: %s", dp.NodeID, sample.Reason)
				}
				value = sample.Value
			}
			result[dp.Key] = fmt.Sprint(value)
		}
		c.addMessageOut()
		return &dtv1.CommandResponsePayload{CommandId: cmd.GetCommandId(), Status: dtv1.CommandStatus_SUCCESS, Message: "opcua query completed", Result: result}, nil
	case dtv1.MessageType_PARAM_UPDATE:
		for _, param := range cmd.GetParamUpdate().GetParams() {
			mapping, ok := c.actionMapping(device, param.GetKey())
			if !ok {
				return rejectedResponse(cmd, dterrors.CodeCommandUnsupported, "param mapping not found: "+param.GetKey()), nil
			}
			if err := client.Write(ctx, mapping.NodeID, dataValueToAny(param.GetValue())); err != nil {
				return nil, err
			}
		}
		c.addMessageOut()
		return &dtv1.CommandResponsePayload{CommandId: cmd.GetCommandId(), Status: dtv1.CommandStatus_SUCCESS, Message: "opcua write completed"}, nil
	case dtv1.MessageType_CONTROL:
		mapping, ok := c.actionMapping(device, cmd.GetControl().GetAction())
		if !ok {
			return rejectedResponse(cmd, dterrors.CodeCommandUnsupported, "action mapping not found"), nil
		}
		switch strings.ToLower(mapping.Type) {
		case "call":
			args := paramsToArgs(cmd.GetControl().GetParams())
			if len(mapping.Inputs) > 0 {
				args = []any{}
				for _, input := range mapping.Inputs {
					v, e := conversion.Scalar(cmd.GetControl().GetParams()[input.Param], input.DataType)
					if e != nil {
						return rejectedResponse(cmd, dterrors.CodeCommandInvalid, e.Error()), nil
					}
					args = append(args, v)
				}
			}
			_, err := client.Call(ctx, mapping.NodeID, mapping.MethodID, args)
			if err != nil {
				return nil, err
			}
		default:
			value := mapping.Value
			if mapping.Param != "" {
				value = cmd.GetControl().GetParams()[mapping.Param]
			}
			var scalar any = value
			if mapping.DataType != "" {
				var err error
				scalar, err = conversion.Scalar(value, mapping.DataType)
				if err != nil {
					return rejectedResponse(cmd, dterrors.CodeCommandInvalid, err.Error()), nil
				}
			}
			if err := client.Write(ctx, mapping.NodeID, scalar); err != nil {
				return nil, err
			}
		}
		c.addMessageOut()
		return &dtv1.CommandResponsePayload{CommandId: cmd.GetCommandId(), Status: dtv1.CommandStatus_SUCCESS, Message: "opcua control completed"}, nil
	default:
		return rejectedResponse(cmd, dterrors.CodeCommandUnsupported, "unsupported command type"), nil
	}
}

func (c *Connector) Stop() error {
	c.mu.RLock()
	cancel, done := c.cancel, c.done
	c.mu.RUnlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("opcua connector shutdown timed out")
	}
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
	return c.Init(cfg)
}

func (c *Connector) poll(ctx context.Context, upstream chan<- *dtv1.DeviceMessage) {
	c.mu.RLock()
	devices := make([]config.DeviceConfig, 0, len(c.deviceConfigs))
	client := c.client
	for _, device := range c.deviceConfigs {
		devices = append(devices, device)
	}
	c.mu.RUnlock()
	if client == nil {
		return
	}
	for _, device := range devices {
		datapoints := make([]*dtv1.Datapoint, 0, len(device.Datapoints))
		failed := false
		for _, dp := range device.Datapoints {
			value, err := client.Read(ctx, dp.NodeID)
			if err != nil {
				c.markError(err)
				failed = true
				value = Observation{Quality: dtv1.DataQuality_BAD, Reason: err.Error(), Timestamp: time.Now(), TimeSource: "collector"}
			}
			datapoints = append(datapoints, makeDatapoint(value, dp))
		}
		if len(datapoints) == 0 {
			continue
		}
		msg := telemetryMessage(c.snapshotConfig(), device, datapoints)
		select {
		case upstream <- msg:
			state := dtv1.DeviceState_ONLINE
			if failed {
				state = dtv1.DeviceState_ERROR
			}
			c.markDevice(device.DeviceID, state)
			c.addMessageIn()
		case <-ctx.Done():
			return
		}
	}
}

func (c *Connector) failedTelemetry(ctx context.Context, err error, upstream chan<- *dtv1.DeviceMessage) {
	cfg := c.snapshotConfig()
	for _, device := range cfg.Devices {
		points := []*dtv1.Datapoint{}
		for _, dp := range device.Datapoints {
			points = append(points, makeDatapoint(Observation{Quality: dtv1.DataQuality_BAD, Reason: err.Error(), Timestamp: time.Now(), TimeSource: "collector"}, dp))
		}
		if len(points) > 0 {
			select {
			case upstream <- telemetryMessage(cfg, device, points):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (c *Connector) messageForNode(nodeID string, value any) (*dtv1.DeviceMessage, error) {
	cfg := c.snapshotConfig()
	for _, device := range cfg.Devices {
		for _, dp := range device.Datapoints {
			if dp.NodeID == nodeID {
				return telemetryMessage(cfg, device, []*dtv1.Datapoint{makeDatapoint(value, dp)}), nil
			}
		}
	}
	return nil, nil
}

func (c *Connector) snapshotConfig() config.ConnectorConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

func (c *Connector) subscriptionNodes() []string {
	cfg := c.snapshotConfig()
	nodes := []string{}
	for _, device := range cfg.Devices {
		for _, dp := range device.Datapoints {
			if dp.NodeID != "" {
				nodes = append(nodes, dp.NodeID)
			}
		}
	}
	return nodes
}

func (c *Connector) actionMapping(device config.DeviceConfig, action string) (config.ActionMapping, bool) {
	if mapping, ok := device.ActionMappings[action]; ok {
		return mapping, true
	}
	c.mu.RLock()
	mapping, ok := c.cfg.ActionMappings[action]
	c.mu.RUnlock()
	return mapping, ok
}

func telemetryMessage(cfg config.ConnectorConfig, device config.DeviceConfig, datapoints []*dtv1.Datapoint) *dtv1.DeviceMessage {
	return &dtv1.DeviceMessage{
		MessageId: fmt.Sprintf("opcua-%s-%d", device.DeviceID, time.Now().UnixNano()),
		Timestamp: time.Now().UnixMilli(),
		Direction: dtv1.Direction_UPSTREAM,
		Device:    &dtv1.DeviceIdentity{DeviceId: device.DeviceID, DeviceName: device.DeviceName, DeviceType: device.DeviceType, ConnectorId: cfg.ConnectorID, Protocol: cfg.Protocol, Tags: mergeTags(cfg.DefaultTags, device.Tags)},
		Type:      dtv1.MessageType_TELEMETRY,
		Payload:   &dtv1.DeviceMessage_Telemetry{Telemetry: &dtv1.TelemetryPayload{Datapoints: datapoints}},
		Metadata:  map[string]string{"protocol": Protocol},
	}
}

func dataValue(value any, dp config.DatapointConfig) *dtv1.DataValue {
	out, _ := conversion.Value(value, dp)
	return out
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

// paramsToArgs 将控制参数转换为 Method 入参。Go map 遍历顺序随机,
// 必须按 key 字典序排序,否则多参数 Method Call 的实参顺序不可重现。
// 约定:参数 key 按字典序对应 OPC-UA Method 的 InputArguments 顺序。
func paramsToArgs(params map[string]string) []any {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		args = append(args, params[key])
	}
	return args
}

func rejectedResponse(cmd *dtv1.DeviceMessage, code, message string) *dtv1.CommandResponsePayload {
	return &dtv1.CommandResponsePayload{CommandId: cmd.GetCommandId(), Status: dtv1.CommandStatus_REJECTED, Message: fmt.Sprintf("%s: %s", code, message)}
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

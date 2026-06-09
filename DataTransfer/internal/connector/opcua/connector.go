package opcua

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	dterrors "competition2026/product/datatransfer/internal/errors"
	"competition2026/product/datatransfer/internal/security"
	gopcua "github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
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
	devices       []dtv1.DeviceInfo
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
	c.devices = make([]dtv1.DeviceInfo, 0, len(cfg.Devices))
	c.deviceConfigs = make(map[string]config.DeviceConfig, len(cfg.Devices))
	now := time.Now()
	for _, device := range cfg.Devices {
		c.deviceConfigs[device.DeviceID] = device
		c.devices = append(c.devices, connector.DeviceInfoFromConfig(cfg.ConnectorID, cfg.Protocol, mergeTags(cfg.DefaultTags, device.Tags), device, dtv1.DeviceState_OFFLINE, now))
	}
	return nil
}

func (c *Connector) Start(ctx context.Context, upstream chan<- *dtv1.DeviceMessage) error {
	client, err := c.clientFactory(c.snapshotConfig())
	if err != nil {
		c.markError(err)
		return err
	}
	if err := client.Connect(ctx); err != nil {
		c.markError(err)
		return err
	}
	c.mu.Lock()
	c.client = client
	c.startedAt = time.Now()
	c.mu.Unlock()
	c.setState(connector.StateRunning, "")

	nodes := c.subscriptionNodes()
	if len(nodes) > 0 {
		go func() {
			err := client.Subscribe(ctx, nodes, func(nodeID string, value any) {
				if msg, buildErr := c.messageForNode(nodeID, value); buildErr == nil && msg != nil {
					select {
					case upstream <- msg:
						c.addMessageIn()
					case <-ctx.Done():
					}
				}
			})
			if err != nil && ctx.Err() == nil {
				c.markError(err)
			}
		}()
	}

	interval := time.Duration(c.snapshotConfig().Polling.IntervalMillis) * time.Millisecond
	if interval <= 0 {
		<-ctx.Done()
	} else {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = client.Close(context.Background())
				c.setState(connector.StateStopped, "")
				return nil
			case <-ticker.C:
				c.poll(ctx, upstream)
			}
		}
	}
	_ = client.Close(context.Background())
	c.setState(connector.StateStopped, "")
	return nil
}

func (c *Connector) SendCommand(ctx context.Context, cmd *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error) {
	c.mu.RLock()
	client := c.client
	device, ok := c.deviceConfigs[cmd.GetDevice().GetDeviceId()]
	c.mu.RUnlock()
	if !ok {
		return rejectedResponse(cmd, dterrors.CodeCommandNoConnector, "device is not managed by this connector"), nil
	}
	if client == nil {
		return nil, fmt.Errorf("%s: opcua client is not connected", dterrors.CodeConnectorRuntime)
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
			_, err := client.Call(ctx, mapping.NodeID, mapping.MethodID, paramsToArgs(cmd.GetControl().GetParams()))
			if err != nil {
				return nil, err
			}
		default:
			value := mapping.Value
			if mapping.Param != "" {
				value = cmd.GetControl().GetParams()[mapping.Param]
			}
			if err := client.Write(ctx, mapping.NodeID, value); err != nil {
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
	c.mu.Lock()
	client := c.client
	c.client = nil
	c.setStateLocked(connector.StateStopped, "")
	c.mu.Unlock()
	if client != nil {
		return client.Close(context.Background())
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

func (c *Connector) Devices() []dtv1.DeviceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]dtv1.DeviceInfo, len(c.devices))
	copy(out, c.devices)
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
		for _, dp := range device.Datapoints {
			value, err := client.Read(ctx, dp.NodeID)
			if err != nil {
				c.markError(err)
				continue
			}
			datapoints = append(datapoints, &dtv1.Datapoint{Key: dp.Key, Value: dataValue(value, dp), Timestamp: time.Now().UnixMilli(), Quality: qualityFromString(dp.Quality), Unit: dp.Unit})
		}
		if len(datapoints) == 0 {
			continue
		}
		msg := telemetryMessage(c.snapshotConfig(), device, datapoints)
		select {
		case upstream <- msg:
			c.markDevice(device.DeviceID, dtv1.DeviceState_ONLINE)
			c.addMessageIn()
		case <-ctx.Done():
			return
		}
	}
}

func (c *Connector) messageForNode(nodeID string, value any) (*dtv1.DeviceMessage, error) {
	cfg := c.snapshotConfig()
	for _, device := range cfg.Devices {
		for _, dp := range device.Datapoints {
			if dp.NodeID == nodeID {
				return telemetryMessage(cfg, device, []*dtv1.Datapoint{{Key: dp.Key, Value: dataValue(value, dp), Timestamp: time.Now().UnixMilli(), Quality: qualityFromString(dp.Quality), Unit: dp.Unit}}), nil
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

type nativeClient struct {
	endpoint string
	client   *gopcua.Client
}

func nativeClientFactory(cfg config.ConnectorConfig) (Client, error) {
	if cfg.Connection.TLS.Enabled {
		if _, err := security.TLSConfig(cfg.Connection.TLS); err != nil {
			return nil, err
		}
	}
	return &nativeClient{endpoint: cfg.Connection.URL}, nil
}

func (c *nativeClient) Connect(ctx context.Context) error {
	client, err := gopcua.NewClient(c.endpoint, gopcua.SecurityMode(ua.MessageSecurityModeNone), gopcua.SecurityPolicy(ua.SecurityPolicyURINone), gopcua.AuthAnonymous())
	if err != nil {
		return err
	}
	c.client = client
	return c.client.Connect(ctx)
}

func (c *nativeClient) Close(ctx context.Context) error {
	if c.client == nil {
		return nil
	}
	return c.client.Close(ctx)
}

func (c *nativeClient) Read(ctx context.Context, nodeID string) (any, error) {
	id, err := ua.ParseNodeID(nodeID)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Read(ctx, &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: id, AttributeID: ua.AttributeIDValue}}, TimestampsToReturn: ua.TimestampsToReturnBoth})
	if err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 || resp.Results[0].Value == nil {
		return nil, fmt.Errorf("%s: opcua read returned no value", dterrors.CodeConnectorRuntime)
	}
	return resp.Results[0].Value.Value(), nil
}

func (c *nativeClient) Write(ctx context.Context, nodeID string, value any) error {
	id, err := ua.ParseNodeID(nodeID)
	if err != nil {
		return err
	}
	variant, err := ua.NewVariant(value)
	if err != nil {
		return err
	}
	_, err = c.client.Write(ctx, &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{NodeID: id, AttributeID: ua.AttributeIDValue, Value: &ua.DataValue{Value: variant}}}})
	return err
}

func (c *nativeClient) Call(ctx context.Context, objectID string, methodID string, args []any) ([]any, error) {
	object, err := ua.ParseNodeID(objectID)
	if err != nil {
		return nil, err
	}
	method, err := ua.ParseNodeID(methodID)
	if err != nil {
		return nil, err
	}
	variants := make([]*ua.Variant, 0, len(args))
	for _, arg := range args {
		variant, err := ua.NewVariant(arg)
		if err != nil {
			return nil, err
		}
		variants = append(variants, variant)
	}
	result, err := c.client.Call(ctx, &ua.CallMethodRequest{ObjectID: object, MethodID: method, InputArguments: variants})
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(result.OutputArguments))
	for _, value := range result.OutputArguments {
		out = append(out, value.Value())
	}
	return out, nil
}

func (c *nativeClient) Subscribe(ctx context.Context, nodes []string, emit func(nodeID string, value any)) error {
	<-ctx.Done()
	return ctx.Err()
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
	switch typed := value.(type) {
	case bool:
		return &dtv1.DataValue{Kind: &dtv1.DataValue_BoolValue{BoolValue: typed}}
	case int:
		return numericValue(float64(typed), dp, true)
	case int16:
		return numericValue(float64(typed), dp, true)
	case int32:
		return numericValue(float64(typed), dp, true)
	case int64:
		return numericValue(float64(typed), dp, true)
	case uint16:
		return numericValue(float64(typed), dp, true)
	case uint32:
		return numericValue(float64(typed), dp, true)
	case float32:
		return numericValue(float64(typed), dp, false)
	case float64:
		return numericValue(typed, dp, false)
	case string:
		if strings.HasPrefix(strings.ToLower(dp.DataType), "int") {
			parsed, _ := strconv.ParseFloat(typed, 64)
			return numericValue(parsed, dp, true)
		}
		return &dtv1.DataValue{Kind: &dtv1.DataValue_StringValue{StringValue: typed}}
	default:
		return &dtv1.DataValue{Kind: &dtv1.DataValue_StringValue{StringValue: fmt.Sprint(value)}}
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

func paramsToArgs(params map[string]string) []any {
	args := make([]any, 0, len(params))
	for _, value := range params {
		args = append(args, value)
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

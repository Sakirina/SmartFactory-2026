package connector

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/command"
	"competition2026/product/datatransfer/internal/config"
)

type Manager struct {
	publisher Publisher
	logger    *slog.Logger
	upstream  chan *dtv1.DeviceMessage

	mu          sync.RWMutex
	connectors  map[string]Connector
	configs     map[string]config.ConnectorConfig
	deviceIndex map[string]Connector
	startCtx    context.Context
	started     bool
	ready       atomic.Bool
	wg          sync.WaitGroup
}

func NewManager(configs []config.ConnectorConfig, publisher Publisher, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	manager := &Manager{
		publisher:   publisher,
		logger:      logger,
		upstream:    make(chan *dtv1.DeviceMessage, 256),
		connectors:  make(map[string]Connector, len(configs)),
		configs:     make(map[string]config.ConnectorConfig, len(configs)),
		deviceIndex: make(map[string]Connector),
	}
	if len(configs) == 0 {
		manager.ready.Store(true)
		return manager, nil
	}
	for _, cfg := range configs {
		factory, ok := factoryFor(cfg.Protocol)
		if !ok {
			return nil, UnknownProtocolError(cfg.Protocol)
		}
		conn := factory()
		if err := conn.Init(cfg); err != nil {
			return nil, fmt.Errorf("init connector %q: %w", cfg.ConnectorID, err)
		}
		manager.connectors[cfg.ConnectorID] = conn
		manager.configs[cfg.ConnectorID] = cloneConnectorConfig(cfg)
	}
	manager.rebuildDeviceIndexLocked()
	manager.ready.Store(true)
	return manager, nil
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		<-ctx.Done()
		return nil
	}
	m.started = true
	m.startCtx = ctx
	for _, conn := range m.connectors {
		m.startConnectorLocked(conn)
	}
	m.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			m.mu.RLock()
			connectors := make([]Connector, 0, len(m.connectors))
			for _, conn := range m.connectors {
				connectors = append(connectors, conn)
			}
			m.mu.RUnlock()
			for _, conn := range connectors {
				if err := conn.Stop(); err != nil {
					m.logger.Warn("connector stop failed", "error", err)
				}
			}
			m.wg.Wait()
			return nil
		case msg := <-m.upstream:
			if err := m.publisher.Publish(msg); err != nil {
				m.logger.Error("publish connector message failed", "error", err, "message_id", msg.GetMessageId())
			}
		}
	}
}

func (m *Manager) Ready() bool {
	return m.ready.Load()
}

func (m *Manager) ApplyConnector(cfg config.ConnectorConfig) error {
	cfg = cloneConnectorConfig(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.connectors[cfg.ConnectorID]
	oldCfg := m.configs[cfg.ConnectorID]
	if exists && oldCfg.Protocol == cfg.Protocol {
		if err := current.ReloadConfig(cfg); err != nil {
			_ = current.ReloadConfig(oldCfg)
			m.rebuildDeviceIndexLocked()
			return fmt.Errorf("reload connector %q: %w", cfg.ConnectorID, err)
		}
		m.configs[cfg.ConnectorID] = cfg
		m.rebuildDeviceIndexLocked()
		return nil
	}
	factory, ok := factoryFor(cfg.Protocol)
	if !ok {
		return UnknownProtocolError(cfg.Protocol)
	}
	next := factory()
	if err := next.Init(cfg); err != nil {
		return fmt.Errorf("init connector %q: %w", cfg.ConnectorID, err)
	}
	if exists {
		m.publishConnectorOfflineLocked(current)
		if err := current.Stop(); err != nil {
			m.logger.Warn("connector stop failed during reload", "connector_id", cfg.ConnectorID, "error", err)
		}
	}
	m.connectors[cfg.ConnectorID] = next
	m.configs[cfg.ConnectorID] = cfg
	m.rebuildDeviceIndexLocked()
	if m.started && m.startCtx != nil && m.startCtx.Err() == nil {
		m.startConnectorLocked(next)
	}
	return nil
}

func (m *Manager) RemoveConnector(connectorID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	conn, ok := m.connectors[connectorID]
	if !ok {
		return fmt.Errorf("connector %q is not registered", connectorID)
	}
	m.publishConnectorOfflineLocked(conn)
	delete(m.connectors, connectorID)
	delete(m.configs, connectorID)
	m.rebuildDeviceIndexLocked()
	return conn.Stop()
}

func (m *Manager) ApplyDevice(connectorID string, device config.DeviceConfig) error {
	m.mu.RLock()
	cfg, ok := m.configs[connectorID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("connector %q is not registered", connectorID)
	}
	replaced := false
	for idx := range cfg.Devices {
		if cfg.Devices[idx].DeviceID == device.DeviceID {
			cfg.Devices[idx] = device
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Devices = append(cfg.Devices, device)
	}
	return m.ApplyConnector(cfg)
}

func (m *Manager) RemoveDevice(connectorID, deviceID string) error {
	m.mu.RLock()
	cfg, ok := m.configs[connectorID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("connector %q is not registered", connectorID)
	}
	next := cfg.Devices[:0]
	found := false
	for _, device := range cfg.Devices {
		if device.DeviceID == deviceID {
			found = true
			m.publishDeviceOffline(connectorID, cfg.Protocol, device)
			continue
		}
		next = append(next, device)
	}
	if !found {
		return fmt.Errorf("device %q is not registered", deviceID)
	}
	cfg.Devices = append([]config.DeviceConfig(nil), next...)
	return m.ApplyConnector(cfg)
}

func (m *Manager) ConnectorConfig(connectorID string) (config.ConnectorConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.configs[connectorID]
	return cloneConnectorConfig(cfg), ok
}

func (m *Manager) ConnectorConfigs() []config.ConnectorConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	configs := make([]config.ConnectorConfig, 0, len(m.configs))
	for _, cfg := range m.configs {
		configs = append(configs, cloneConnectorConfig(cfg))
	}
	return configs
}

func (m *Manager) ResolveDevice(deviceID string) (command.Executor, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	conn, ok := m.deviceIndex[deviceID]
	return conn, ok
}

func (m *Manager) ListDevices(req *dtv1.ListDevicesRequest) *dtv1.ListDevicesResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()
	resp := &dtv1.ListDevicesResponse{}
	for _, conn := range m.connectors {
		for _, device := range conn.Devices() {
			if matchesDevice(req, &device) {
				copyDevice := device
				resp.Devices = append(resp.Devices, &copyDevice)
			}
		}
	}
	return resp
}

func (m *Manager) Snapshot() (connectedDevices int32, activeConnectors int32, metrics map[string]*dtv1.ConnectorMetrics) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	metrics = make(map[string]*dtv1.ConnectorMetrics, len(m.connectors))
	for id, conn := range m.connectors {
		status := conn.Status()
		metrics[id] = StatusMetrics(status)
		if status.State == StateRunning {
			activeConnectors++
		}
		for _, device := range conn.Devices() {
			if device.State == dtv1.DeviceState_ONLINE {
				connectedDevices++
			}
		}
	}
	return connectedDevices, activeConnectors, metrics
}

func (m *Manager) startConnectorLocked(conn Connector) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		if err := conn.Start(m.startCtx, m.upstream); err != nil && m.startCtx.Err() == nil {
			m.logger.Error("connector exited with error", "error", err)
		}
	}()
}

func (m *Manager) rebuildDeviceIndexLocked() {
	m.deviceIndex = make(map[string]Connector)
	for _, conn := range m.connectors {
		for _, device := range conn.Devices() {
			deviceID := device.GetIdentity().GetDeviceId()
			if deviceID != "" {
				m.deviceIndex[deviceID] = conn
			}
		}
	}
}

func (m *Manager) publishConnectorOfflineLocked(conn Connector) {
	for _, device := range conn.Devices() {
		m.publishOfflineInfo(device)
	}
}

func (m *Manager) publishDeviceOffline(connectorID, protocol string, device config.DeviceConfig) {
	info := DeviceInfoFromConfig(connectorID, protocol, mergeTags(nil, device.Tags), device, dtv1.DeviceState_OFFLINE, now())
	m.publishOfflineInfo(info)
}

func (m *Manager) publishOfflineInfo(info dtv1.DeviceInfo) {
	if m.publisher == nil {
		return
	}
	msg := &dtv1.DeviceMessage{
		MessageId: fmt.Sprintf("status-%s-%d", info.GetIdentity().GetDeviceId(), now().UnixNano()),
		Timestamp: now().UnixMilli(),
		Direction: dtv1.Direction_UPSTREAM,
		Device:    info.GetIdentity(),
		Type:      dtv1.MessageType_STATUS,
		Payload: &dtv1.DeviceMessage_Status{
			Status: &dtv1.StatusPayload{
				State:    dtv1.DeviceState_OFFLINE,
				Reason:   "configuration removed",
				LastSeen: now().UnixMilli(),
			},
		},
	}
	if err := m.publisher.Publish(msg); err != nil {
		m.logger.Warn("publish offline status failed", "device_id", info.GetIdentity().GetDeviceId(), "error", err)
	}
}

func matchesDevice(req *dtv1.ListDevicesRequest, device *dtv1.DeviceInfo) bool {
	if req == nil {
		return true
	}
	identity := device.GetIdentity()
	if req.GetConnectorId() != "" && identity.GetConnectorId() != req.GetConnectorId() {
		return false
	}
	if req.GetState() != dtv1.DeviceState_STATE_UNSPECIFIED && device.GetState() != req.GetState() {
		return false
	}
	for key, expected := range req.GetTagMatch() {
		if identity.GetTags()[key] != expected {
			return false
		}
	}
	return true
}

func cloneConnectorConfig(cfg config.ConnectorConfig) config.ConnectorConfig {
	cfg.DefaultTags = cloneStringMap(cfg.DefaultTags)
	cfg.ActionMappings = cloneActionMappings(cfg.ActionMappings)
	cfg.Devices = append([]config.DeviceConfig(nil), cfg.Devices...)
	for idx := range cfg.Devices {
		cfg.Devices[idx].Tags = cloneStringMap(cfg.Devices[idx].Tags)
		cfg.Devices[idx].Datapoints = append([]config.DatapointConfig(nil), cfg.Devices[idx].Datapoints...)
		cfg.Devices[idx].ActionMappings = cloneActionMappings(cfg.Devices[idx].ActionMappings)
		if cfg.Devices[idx].Address != nil {
			cfg.Devices[idx].Address = append([]byte(nil), cfg.Devices[idx].Address...)
		}
	}
	return cfg
}

func cloneActionMappings(in map[string]config.ActionMapping) map[string]config.ActionMapping {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]config.ActionMapping, len(in))
	for key, value := range in {
		value.Values = append([]string(nil), value.Values...)
		out[key] = value
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func mergeTags(base, override map[string]string) map[string]string {
	out := cloneStringMap(base)
	if out == nil {
		out = make(map[string]string, len(override))
	}
	for key, value := range override {
		out[key] = value
	}
	return out
}

func now() time.Time {
	return time.Now()
}

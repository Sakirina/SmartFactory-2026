package connector

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

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
	deviceIndex map[string]Connector
	started     bool
	ready       atomic.Bool
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
		for _, device := range conn.Devices() {
			manager.deviceIndex[device.GetIdentity().GetDeviceId()] = conn
		}
	}
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
	connectors := make([]Connector, 0, len(m.connectors))
	for _, conn := range m.connectors {
		connectors = append(connectors, conn)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, conn := range connectors {
		conn := conn
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := conn.Start(ctx, m.upstream); err != nil && ctx.Err() == nil {
				m.logger.Error("connector exited with error", "error", err)
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()

	for {
		select {
		case <-ctx.Done():
			for _, conn := range connectors {
				if err := conn.Stop(); err != nil {
					m.logger.Warn("connector stop failed", "error", err)
				}
			}
			<-done
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

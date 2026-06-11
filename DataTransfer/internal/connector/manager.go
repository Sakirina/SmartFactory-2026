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
	dterrors "competition2026/product/datatransfer/internal/errors"
)

// restartBaseDelay/restartMaxDelay 控制 Connector 异常退出后的自动重启退避(DT-CON-003)。
const (
	restartBaseDelay = time.Second
	restartMaxDelay  = time.Minute
)

type Manager struct {
	publisher Publisher
	logger    *slog.Logger
	upstream  chan *dtv1.DeviceMessage

	mu          sync.RWMutex
	connectors  map[string]Connector
	configs     map[string]config.ConnectorConfig
	deviceIndex map[string]Connector
	// 上报策略索引(FR-S-035):随配置变更与 deviceIndex 一并重建。
	connectorStrategy map[string]config.ReportStrategyConfig
	datapointStrategy map[datapointKey]config.ReportStrategyConfig
	startCtx          context.Context
	started           bool
	ready             atomic.Bool
	wg                sync.WaitGroup

	// 背压观测(FR-S-037~039):基于 upstream 通道使用率的水位状态。
	bpActive       atomic.Bool
	bpTriggerTotal atomic.Int64
	bpDroppedTotal atomic.Int64
	bpPolicy       atomic.Int32 // dtv1.BackpressurePolicy
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
			m.observeBackpressure()
			if m.shouldDropForBackpressure(msg) {
				continue
			}
			if err := m.publisher.Publish(msg); err != nil {
				m.logger.Error("publish connector message failed", "error", err, "message_id", msg.GetMessageId())
			}
		}
	}
}

// SetBackpressurePolicy 设置背压策略(BP_BLOCK 为默认行为:通道写满时 Connector 自然阻塞)。
// BP_DEGRADE 需要 Connector 支持动态采集降频,当前版本回退为 BP_BLOCK 并记录警告。
func (m *Manager) SetBackpressurePolicy(policy dtv1.BackpressurePolicy) {
	if policy == dtv1.BackpressurePolicy_BP_DEGRADE {
		m.logger.Warn("BP_DEGRADE is not supported by built-in connectors yet; falling back to BP_BLOCK",
			"code", dterrors.CodeStrategyApplied)
		policy = dtv1.BackpressurePolicy_BP_BLOCK
	}
	m.bpPolicy.Store(int32(policy))
	m.logger.Info("backpressure policy applied", "code", dterrors.CodeStrategyApplied, "policy", policy.String())
}

// observeBackpressure 按三级水位(80% 触发 / 50% 解除)记录背压状态(FR-S-039:不得静默发生)。
func (m *Manager) observeBackpressure() {
	usage := m.QueueUsagePercent()
	if usage >= 80 && !m.bpActive.Load() {
		m.bpActive.Store(true)
		m.bpTriggerTotal.Add(1)
		m.logger.Warn("backpressure triggered",
			"code", dterrors.CodeBackpressureOn,
			"queue_usage_percent", usage,
			"policy", m.BackpressurePolicy().String(),
		)
	} else if usage <= 50 && m.bpActive.Load() {
		m.bpActive.Store(false)
		m.logger.Info("backpressure released", "code", dterrors.CodeBackpressureOff, "queue_usage_percent", usage)
	}
}

// shouldDropForBackpressure 在 BP_DROP_OLDEST 策略且背压激活时丢弃队首(最旧)遥测消息。
// STATUS/EVENT/CMD_RESPONSE 优先级高于遥测,任何策略下都不丢弃。
func (m *Manager) shouldDropForBackpressure(msg *dtv1.DeviceMessage) bool {
	if !m.bpActive.Load() {
		return false
	}
	if m.BackpressurePolicy() != dtv1.BackpressurePolicy_BP_DROP_OLDEST {
		return false
	}
	if msg.GetType() != dtv1.MessageType_TELEMETRY {
		return false
	}
	m.bpDroppedTotal.Add(1)
	m.logger.Warn("telemetry dropped by backpressure policy",
		"code", dterrors.CodeBufferFull,
		"message_id", msg.GetMessageId(),
		"device_id", msg.GetDevice().GetDeviceId(),
	)
	return true
}

func (m *Manager) QueueUsagePercent() float64 {
	capacity := cap(m.upstream)
	if capacity == 0 {
		return 0
	}
	return float64(len(m.upstream)) * 100 / float64(capacity)
}

// BackpressureSnapshot 返回背压观测状态:是否激活、策略、队列使用率、触发次数、丢弃总数。
func (m *Manager) BackpressureSnapshot() (bool, dtv1.BackpressurePolicy, float64, int64, int64) {
	return m.bpActive.Load(), m.BackpressurePolicy(), m.QueueUsagePercent(), m.bpTriggerTotal.Load(), m.bpDroppedTotal.Load()
}

func (m *Manager) BackpressurePolicy() dtv1.BackpressurePolicy {
	policy := dtv1.BackpressurePolicy(m.bpPolicy.Load())
	if policy == dtv1.BackpressurePolicy_BP_UNSPECIFIED {
		return dtv1.BackpressurePolicy_BP_BLOCK
	}
	return policy
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
		if err := current.ReloadConfig(cloneConnectorConfig(cfg)); err != nil {
			_ = current.ReloadConfig(cloneConnectorConfig(oldCfg))
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
	if err := next.Init(cloneConnectorConfig(cfg)); err != nil {
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
	stored, ok := m.configs[connectorID]
	// 必须先克隆再修改:configs 中的切片与 map 不能被就地修改,
	// 否则会与并发读者产生数据竞争,且 ApplyConnector 失败回滚时旧配置已被污染。
	cfg := cloneConnectorConfig(stored)
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
	stored, ok := m.configs[connectorID]
	cfg := cloneConnectorConfig(stored)
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("connector %q is not registered", connectorID)
	}
	next := make([]config.DeviceConfig, 0, len(cfg.Devices))
	found := false
	var removed config.DeviceConfig
	for _, device := range cfg.Devices {
		if device.DeviceID == deviceID {
			found = true
			removed = device
			continue
		}
		next = append(next, device)
	}
	if !found {
		return fmt.Errorf("device %q is not registered", deviceID)
	}
	cfg.Devices = next
	if err := m.ApplyConnector(cfg); err != nil {
		return err
	}
	// 仅在变更成功后再发布离线状态,避免失败回滚时上游收到虚假离线。
	m.publishDeviceOffline(connectorID, cfg.Protocol, removed)
	return nil
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
			if matchesDevice(req, device) {
				resp.Devices = append(resp.Devices, device)
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

// startConnectorLocked 启动 Connector 并守护其生命周期:
// panic 被恢复(NFR-008 进程内隔离),异常退出按指数退避自动重启(DT-CON-003),
// 直至 ctx 取消或该 Connector 实例被替换/移除。
func (m *Manager) startConnectorLocked(conn Connector) {
	ctx := m.startCtx
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		delay := restartBaseDelay
		for ctx.Err() == nil {
			err := m.runConnector(ctx, conn)
			if ctx.Err() != nil || !m.isCurrent(conn) {
				return
			}
			if err == nil {
				// Start 正常返回但 ctx 未取消:视为协议层自行退出,同样按退避重启。
				err = fmt.Errorf("connector start returned before shutdown")
			}
			m.logger.Error("connector exited; scheduling restart",
				"code", dterrors.CodeConnectorCrashed,
				"error", err,
				"restart_in", delay.String(),
			)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if delay *= 2; delay > restartMaxDelay {
				delay = restartMaxDelay
			}
			if !m.isCurrent(conn) {
				return
			}
		}
	}()
}

func (m *Manager) runConnector(ctx context.Context, conn Connector) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("connector panicked: %v", recovered)
		}
	}()
	return conn.Start(ctx, m.upstream)
}

// isCurrent 判断该实例是否仍是注册表中的当前 Connector(被热替换后旧实例不再重启)。
func (m *Manager) isCurrent(conn Connector) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, current := range m.connectors {
		if current == conn {
			return true
		}
	}
	return false
}

type datapointKey struct {
	connectorID string
	deviceID    string
	key         string
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
	m.connectorStrategy = make(map[string]config.ReportStrategyConfig, len(m.configs))
	m.datapointStrategy = make(map[datapointKey]config.ReportStrategyConfig)
	for connectorID, cfg := range m.configs {
		m.connectorStrategy[connectorID] = cfg.ReportStrategy
		for _, device := range cfg.Devices {
			for _, dp := range device.Datapoints {
				if dp.Strategy != nil {
					m.datapointStrategy[datapointKey{connectorID, device.DeviceID, dp.Key}] = *dp.Strategy
				}
			}
		}
	}
}

// StrategyFor 实现 strategy.Resolver:返回数据点级与 Connector 级策略(可为 nil)。
func (m *Manager) StrategyFor(connectorID, deviceID, key string) (*config.ReportStrategyConfig, *config.ReportStrategyConfig) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var dpLevel *config.ReportStrategyConfig
	if strategy, ok := m.datapointStrategy[datapointKey{connectorID, deviceID, key}]; ok {
		dpLevel = &strategy
	}
	var connLevel *config.ReportStrategyConfig
	if strategy, ok := m.connectorStrategy[connectorID]; ok {
		connLevel = &strategy
	}
	return dpLevel, connLevel
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

func (m *Manager) publishOfflineInfo(info *dtv1.DeviceInfo) {
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

// Package runtime 是消息中枢:接收 Connector 上行消息,经上报策略过滤后
// 交付北向 sink、近期环形缓冲与订阅者;承接下行指令与配置推送的入口,
// 并汇总全局指标(速率、背压、策略、缓冲)供管理端与 GetMetrics 使用。
package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/command"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/configmanager"
	"competition2026/product/datatransfer/internal/connector"
	dterrors "competition2026/product/datatransfer/internal/errors"
	"competition2026/product/datatransfer/internal/strategy"
)

type Runtime struct {
	cfg        config.Config
	commands   *command.Service
	connectors *connector.Manager
	configs    *configmanager.Manager
	upstream   UpstreamSink
	buffer     PersistentBufferProvider
	store      *ringStore
	strategy   *strategy.Engine

	mu            sync.RWMutex
	subscribers   map[uint64]subscription
	nextSubID     uint64
	grpcServing   bool
	mqttConnected bool

	upstreamTotal         atomic.Int64
	downstreamTotal       atomic.Int64
	rejectedCommandTotal  atomic.Int64
	duplicateCommandTotal atomic.Int64
	configRejectTotal     atomic.Int64
	discoveryEventTotal   atomic.Int64
	subscriberDropTotal   atomic.Int64

	// 速率窗口:供 MetricsResponse 计算条/秒(FR-S-030 要求速率而非累计值)。
	rateMu     sync.Mutex
	rateAt     time.Time
	rateUp     int64
	rateDown   int64
	upPerSec   float64
	downPerSec float64
}

type subscription struct {
	filter Filter
	ch     chan *dtv1.DeviceMessage
}

type UpstreamSink interface {
	HandleUpstream(ctx context.Context, msg *dtv1.DeviceMessage) error
}

type PersistentBufferProvider interface {
	BufferSnapshot() PersistentBufferSnapshot
}

type PersistentBufferSnapshot struct {
	Pending          int64
	Sending          int64
	Completed        int64
	Dropped          int64
	Retry            int64
	LastErrorCount   int64
	CapacityBytes    int64
	UsedBytes        int64
	UsagePercent     float64
	ReplayBatchTotal int64
}

type Snapshot struct {
	Timestamp             int64
	Ready                 bool
	GRPCServing           bool
	MQTTConnected         bool
	BufferSize            int64
	BufferUsagePercent    float64
	ConnectedDevices      int32
	ActiveConnectors      int32
	UpstreamTotal         int64
	DownstreamTotal       int64
	RejectedCommandTotal  int64
	DuplicateCommandTotal int64
	ConfigRejectTotal     int64
	DiscoveryEventTotal   int64
	SubscriberDropTotal   int64
	PersistentBuffer      PersistentBufferSnapshot

	// 背压观测(FR-S-039)
	BackpressureActive       bool
	BackpressurePolicy       dtv1.BackpressurePolicy
	QueueUsagePercent        float64
	BackpressureTriggerTotal int64
	BackpressureDropTotal    int64

	// 上报策略统计(FR-S-030)
	StrategyFilteredMessages int64
	StrategyDeliveredCount   int64
	StrategyFilteredPoints   int64

	Connectors map[string]*dtv1.ConnectorMetrics
}

func New(cfg config.Config) *Runtime {
	retry := command.RetryPolicy{
		Mode:        cfg.Runtime.CommandRetry.IntervalMode,
		Interval:    time.Duration(cfg.Runtime.CommandRetry.IntervalMS) * time.Millisecond,
		MaxInterval: time.Duration(cfg.Runtime.CommandRetry.MaxIntervalMS) * time.Millisecond,
	}
	return &Runtime{
		cfg:         cfg,
		commands:    command.NewServiceWithRetry(time.Duration(cfg.Runtime.CommandTTLSeconds)*time.Second, retry),
		store:       newRingStore(cfg.Runtime.RingSize),
		subscribers: make(map[uint64]subscription),
		strategy:    strategy.NewEngine(cfg.ReportStrategy),
		rateAt:      time.Now(),
	}
}

func (r *Runtime) Config() config.Config {
	return r.cfg
}

func (r *Runtime) AttachConnectorManager(manager *connector.Manager) {
	r.mu.Lock()
	r.connectors = manager
	r.mu.Unlock()
	r.commands.SetResolver(manager)
	r.strategy.SetResolver(manager)
	if policy, ok := dtv1.BackpressurePolicy_value[r.cfg.Backpressure.Policy]; ok && policy != 0 {
		manager.SetBackpressurePolicy(dtv1.BackpressurePolicy(policy))
	}
}

// ApplyGlobalConfig 承接 UPDATE_GLOBAL 推送:热更新全局上报策略与背压策略。
// queue_capacity 当前为启动期参数,运行时不支持调整,推送非零值时记录警告并忽略。
func (r *Runtime) ApplyGlobalConfig(payload *dtv1.GlobalConfigPayload) error {
	if payload == nil {
		return fmt.Errorf("global config payload is required")
	}
	if strategyCfg := payload.GetDefaultReportStrategy(); strategyCfg != nil {
		r.strategy.SetGlobal(config.ReportStrategyConfig{
			Mode:          strategyCfg.GetMode().String(),
			PeriodSeconds: int(strategyCfg.GetPeriodSeconds()),
			Deadband:      strategyCfg.GetDeadband(),
		})
		slog.Info("global report strategy applied",
			"code", dterrors.CodeStrategyApplied,
			"mode", strategyCfg.GetMode().String(),
			"period_seconds", strategyCfg.GetPeriodSeconds(),
			"deadband", strategyCfg.GetDeadband(),
		)
	}
	if policy := payload.GetBackpressurePolicy(); policy != dtv1.BackpressurePolicy_BP_UNSPECIFIED {
		r.mu.RLock()
		manager := r.connectors
		r.mu.RUnlock()
		if manager == nil {
			return fmt.Errorf("connector manager is not attached")
		}
		manager.SetBackpressurePolicy(policy)
	}
	if payload.GetQueueCapacity() != 0 {
		slog.Warn("queue_capacity hot update is not supported; restart with runtime.ring_size instead",
			"code", dterrors.CodeConfigWarning,
			"requested", payload.GetQueueCapacity(),
		)
	}
	return nil
}

func (r *Runtime) AttachConfigManager(manager *configmanager.Manager) {
	r.mu.Lock()
	r.configs = manager
	r.mu.Unlock()
}

func (r *Runtime) AttachUpstreamSink(sink UpstreamSink) {
	r.mu.Lock()
	r.upstream = sink
	r.mu.Unlock()
}

func (r *Runtime) AttachPersistentBuffer(provider PersistentBufferProvider) {
	r.mu.Lock()
	r.buffer = provider
	r.mu.Unlock()
}

func (r *Runtime) Publish(msg *dtv1.DeviceMessage) error {
	if msg == nil {
		return fmt.Errorf("%s: message is nil", dterrors.CodeRuntimeInvalid)
	}
	if msg.Timestamp == 0 {
		msg.Timestamp = nowMillis()
	}
	// 上报策略仅过滤上行 TELEMETRY(FR-S-036);整条被过滤时静默成功,计入策略指标。
	if msg.Direction == dtv1.Direction_UPSTREAM && msg.Type == dtv1.MessageType_TELEMETRY {
		msg = r.strategy.Apply(msg)
		if msg == nil {
			return nil
		}
	}
	if msg.Direction == dtv1.Direction_UPSTREAM {
		r.mu.RLock()
		sink := r.upstream
		r.mu.RUnlock()
		if sink != nil {
			if err := sink.HandleUpstream(context.Background(), msg); err != nil {
				return err
			}
		}
	}
	r.store.Add(msg)
	if msg.Direction == dtv1.Direction_UPSTREAM {
		r.upstreamTotal.Add(1)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, sub := range r.subscribers {
		if !sub.filter.Match(msg) {
			continue
		}
		select {
		case sub.ch <- msg:
		default:
			// 订阅者消费过慢导致的丢弃必须可见(FR-S-039),通过计数与指标暴露。
			r.subscriberDropTotal.Add(1)
		}
	}
	return nil
}

func (r *Runtime) Subscribe(filter Filter) (<-chan *dtv1.DeviceMessage, func()) {
	id := atomic.AddUint64((*uint64)(&r.nextSubID), 1)
	ch := make(chan *dtv1.DeviceMessage, 64)

	r.mu.Lock()
	r.subscribers[id] = subscription{filter: filter, ch: ch}
	r.mu.Unlock()

	cancel := func() {
		r.mu.Lock()
		if sub, ok := r.subscribers[id]; ok {
			delete(r.subscribers, id)
			close(sub.ch)
		}
		r.mu.Unlock()
	}
	return ch, cancel
}

func (r *Runtime) Pull(req *dtv1.PullRequest) *dtv1.DeviceMessageBatch {
	if req == nil {
		req = &dtv1.PullRequest{}
	}
	filter := Filter{
		Types: typeSet(req.Types, nil),
	}
	messages := r.store.Since(req.SinceTimestamp, int(req.MaxCount), filter)
	return &dtv1.DeviceMessageBatch{
		Messages:  messages,
		BatchId:   fmt.Sprintf("pull-%d", time.Now().UnixNano()),
		CreatedAt: nowMillis(),
	}
}

func (r *Runtime) HandleCommand(ctx context.Context, msg *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, bool, error) {
	result, err := r.commands.Handle(ctx, msg)
	if err != nil {
		r.rejectedCommandTotal.Add(1)
		return nil, false, err
	}
	r.downstreamTotal.Add(1)
	if result.Duplicate {
		r.duplicateCommandTotal.Add(1)
	}
	if result.Response.GetStatus() == dtv1.CommandStatus_REJECTED {
		r.rejectedCommandTotal.Add(1)
	}
	return result.Response, result.Duplicate, nil
}

func (r *Runtime) AcceptCommandAsync(ctx context.Context, msg *dtv1.DeviceMessage) (*dtv1.CommandAccepted, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accepted, err := r.commands.HandleAsync(msg, func(response *dtv1.CommandResponsePayload) {
		r.countCommandResponse(response, false)
		if publishErr := r.Publish(commandResponseMessage(msg, response)); publishErr != nil {
			r.rejectedCommandTotal.Add(1)
		}
	})
	if err != nil {
		if errors.Is(err, command.ErrDuplicate) {
			r.duplicateCommandTotal.Add(1)
			r.rejectedCommandTotal.Add(1)
		} else {
			r.rejectedCommandTotal.Add(1)
		}
		return nil, err
	}
	r.downstreamTotal.Add(1)
	return accepted, nil
}

func (r *Runtime) RejectConfig(update *dtv1.DeviceConfigUpdate) *dtv1.ConfigUpdateResponse {
	r.configRejectTotal.Add(1)
	updateID := ""
	if update != nil {
		updateID = update.UpdateId
	}
	return &dtv1.ConfigUpdateResponse{
		Success:      false,
		ErrorMessage: fmt.Sprintf("%s: configuration manager is not attached", dterrors.CodeConfigRejected),
		UpdateId:     updateID,
	}
}

func (r *Runtime) ApplyConfig(update *dtv1.DeviceConfigUpdate) *dtv1.ConfigUpdateResponse {
	r.mu.RLock()
	manager := r.configs
	r.mu.RUnlock()
	if manager == nil {
		return r.RejectConfig(update)
	}
	response := manager.Apply(update)
	if !response.GetSuccess() {
		r.configRejectTotal.Add(1)
	}
	return response
}

func (r *Runtime) ListDevices(req *dtv1.ListDevicesRequest) *dtv1.ListDevicesResponse {
	r.mu.RLock()
	manager := r.connectors
	r.mu.RUnlock()
	if manager != nil {
		return manager.ListDevices(req)
	}
	return &dtv1.ListDevicesResponse{}
}

func (r *Runtime) MetricsResponse() *dtv1.MetricsResponse {
	snapshot := r.Snapshot()
	bufferSize := snapshot.BufferSize
	bufferUsage := snapshot.BufferUsagePercent
	if r.cfg.RunMode == config.RunModeSplit {
		bufferSize = snapshot.PersistentBuffer.Pending + snapshot.PersistentBuffer.Sending
		bufferUsage = snapshot.PersistentBuffer.UsagePercent
	}
	upRate, downRate := r.rates(snapshot.UpstreamTotal, snapshot.DownstreamTotal)
	delivered := snapshot.StrategyDeliveredCount
	filtered := snapshot.StrategyFilteredMessages
	passThrough := 1.0
	if delivered+filtered > 0 {
		passThrough = float64(delivered) / float64(delivered+filtered)
	}
	return &dtv1.MetricsResponse{
		Timestamp:               snapshot.Timestamp,
		ConnectedDevices:        snapshot.ConnectedDevices,
		ActiveConnectors:        snapshot.ActiveConnectors,
		UpstreamMsgPerSec:       upRate,
		DownstreamCmdPerSec:     downRate,
		BufferSize:              bufferSize,
		BufferUsagePercent:      bufferUsage,
		BackpressureActive:      snapshot.BackpressureActive,
		BackpressurePolicy:      snapshot.BackpressurePolicy,
		QueueUsagePercent:       snapshot.QueueUsagePercent,
		StrategyFilteredCount:   filtered,
		StrategyPassThroughRate: passThrough,
		Connectors:              snapshot.Connectors,
	}
}

// rates 以两次调用之间的增量计算条/秒;距上次窗口不足 1s 时沿用上次速率,避免抖动。
func (r *Runtime) rates(upTotal, downTotal int64) (float64, float64) {
	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	now := time.Now()
	elapsed := now.Sub(r.rateAt).Seconds()
	if elapsed >= 1 {
		r.upPerSec = float64(upTotal-r.rateUp) / elapsed
		r.downPerSec = float64(downTotal-r.rateDown) / elapsed
		r.rateUp = upTotal
		r.rateDown = downTotal
		r.rateAt = now
	}
	return r.upPerSec, r.downPerSec
}

func (r *Runtime) Snapshot() Snapshot {
	r.mu.RLock()
	grpcServing := r.grpcServing
	mqttConnected := r.mqttConnected
	manager := r.connectors
	r.mu.RUnlock()

	bufferSize, bufferUsage := r.store.Stats()
	connectedDevices, activeConnectors, connectorMetrics := r.connectorSnapshot()
	persistentBuffer := r.persistentBufferSnapshot()
	if r.cfg.RunMode == config.RunModeSplit && persistentBuffer.CapacityBytes > 0 {
		bufferSize = int(persistentBuffer.Pending + persistentBuffer.Sending)
		bufferUsage = persistentBuffer.UsagePercent
	}
	snapshot := Snapshot{
		Timestamp:             nowMillis(),
		Ready:                 r.ready(grpcServing, mqttConnected),
		GRPCServing:           grpcServing,
		MQTTConnected:         mqttConnected,
		BufferSize:            int64(bufferSize),
		BufferUsagePercent:    bufferUsage,
		ConnectedDevices:      connectedDevices,
		ActiveConnectors:      activeConnectors,
		UpstreamTotal:         r.upstreamTotal.Load(),
		DownstreamTotal:       r.downstreamTotal.Load(),
		RejectedCommandTotal:  r.rejectedCommandTotal.Load(),
		DuplicateCommandTotal: r.duplicateCommandTotal.Load(),
		ConfigRejectTotal:     r.configRejectTotal.Load(),
		DiscoveryEventTotal:   r.discoveryEventTotal.Load(),
		SubscriberDropTotal:   r.subscriberDropTotal.Load(),
		PersistentBuffer:      persistentBuffer,
		BackpressurePolicy:    dtv1.BackpressurePolicy_BP_BLOCK,
		Connectors:            connectorMetrics,
	}
	if manager != nil {
		active, policy, queueUsage, triggers, drops := manager.BackpressureSnapshot()
		snapshot.BackpressureActive = active
		snapshot.BackpressurePolicy = policy
		snapshot.QueueUsagePercent = queueUsage
		snapshot.BackpressureTriggerTotal = triggers
		snapshot.BackpressureDropTotal = drops
	}
	filteredMessages, deliveredMessages, filteredPoints := r.strategy.Stats()
	snapshot.StrategyFilteredMessages = filteredMessages
	snapshot.StrategyDeliveredCount = deliveredMessages
	snapshot.StrategyFilteredPoints = filteredPoints
	return snapshot
}

func (r *Runtime) SetGRPCServing(serving bool) {
	r.mu.Lock()
	r.grpcServing = serving
	r.mu.Unlock()
}

func (r *Runtime) SetMQTTConnected(connected bool) {
	r.mu.Lock()
	r.mqttConnected = connected
	r.mu.Unlock()
}

func (r *Runtime) Ready() bool {
	s := r.Snapshot()
	return s.Ready
}

func (r *Runtime) ready(grpcServing, mqttConnected bool) bool {
	connectorsReady := true
	r.mu.RLock()
	manager := r.connectors
	r.mu.RUnlock()
	if manager != nil {
		connectorsReady = manager.Ready()
	}
	if !connectorsReady {
		return false
	}
	switch r.cfg.RunMode {
	case config.RunModeSplit:
		return mqttConnected
	default:
		return grpcServing
	}
}

func (r *Runtime) RecordDiscovery(event DiscoveryEvent) {
	r.discoveryEventTotal.Add(1)
	slog.Info(
		"discovery event recorded",
		"device_id", event.DeviceID,
		"device_name", event.DeviceName,
		"device_type", event.DeviceType,
		"protocol", event.Protocol,
		"connector_id", event.ConnectorID,
		"observed_at", event.ObservedAt,
	)
}

type DiscoveryEvent struct {
	DeviceID    string
	DeviceName  string
	DeviceType  string
	Protocol    string
	ConnectorID string
	ObservedAt  int64
	Metadata    map[string]string
}

func (r *Runtime) connectorSnapshot() (int32, int32, map[string]*dtv1.ConnectorMetrics) {
	r.mu.RLock()
	manager := r.connectors
	r.mu.RUnlock()
	if manager == nil {
		return 0, 0, map[string]*dtv1.ConnectorMetrics{}
	}
	return manager.Snapshot()
}

func (r *Runtime) persistentBufferSnapshot() PersistentBufferSnapshot {
	r.mu.RLock()
	provider := r.buffer
	r.mu.RUnlock()
	if provider == nil {
		return PersistentBufferSnapshot{}
	}
	return provider.BufferSnapshot()
}

func (r *Runtime) countCommandResponse(response *dtv1.CommandResponsePayload, duplicate bool) {
	if duplicate {
		r.duplicateCommandTotal.Add(1)
	}
	if response.GetStatus() == dtv1.CommandStatus_REJECTED {
		r.rejectedCommandTotal.Add(1)
	}
}

func nowMillis() int64 {
	return time.Now().UnixNano() / int64(time.Millisecond)
}

func commandResponseMessage(cmd *dtv1.DeviceMessage, response *dtv1.CommandResponsePayload) *dtv1.DeviceMessage {
	return &dtv1.DeviceMessage{
		MessageId: fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		Timestamp: nowMillis(),
		Direction: dtv1.Direction_UPSTREAM,
		Device:    cmd.GetDevice(),
		Type:      dtv1.MessageType_CMD_RESPONSE,
		CommandId: cmd.GetCommandId(),
		Payload: &dtv1.DeviceMessage_CmdResponse{
			CmdResponse: response,
		},
		Metadata: map[string]string{
			"origin": "command-router",
		},
	}
}

func IsDuplicateCommand(err error) bool {
	return errors.Is(err, command.ErrDuplicate)
}

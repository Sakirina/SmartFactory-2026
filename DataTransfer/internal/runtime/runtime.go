package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/command"
	"competition2026/product/datatransfer/internal/config"
	dterrors "competition2026/product/datatransfer/internal/errors"
)

type Runtime struct {
	cfg      config.Config
	commands *command.Service
	store    *ringStore

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
}

type subscription struct {
	filter Filter
	ch     chan *dtv1.DeviceMessage
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
}

func New(cfg config.Config) *Runtime {
	return &Runtime{
		cfg:         cfg,
		commands:    command.NewService(time.Duration(cfg.Runtime.CommandTTLSeconds) * time.Second),
		store:       newRingStore(cfg.Runtime.RingSize),
		subscribers: make(map[uint64]subscription),
	}
}

func (r *Runtime) Config() config.Config {
	return r.cfg
}

func (r *Runtime) Publish(msg *dtv1.DeviceMessage) error {
	if msg == nil {
		return fmt.Errorf("%s: message is nil", dterrors.CodeRuntimeInvalid)
	}
	if msg.Timestamp == 0 {
		msg.Timestamp = nowMillis()
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
	response, duplicate, err := r.HandleCommand(ctx, msg)
	if err != nil {
		return nil, err
	}
	if duplicate {
		return nil, command.ErrDuplicate
	}
	if err := r.Publish(commandResponseMessage(msg, response)); err != nil {
		return nil, err
	}
	return &dtv1.CommandAccepted{
		CommandId:  msg.CommandId,
		AcceptedAt: nowMillis(),
	}, nil
}

func (r *Runtime) RejectConfig(update *dtv1.DeviceConfigUpdate) *dtv1.ConfigUpdateResponse {
	r.configRejectTotal.Add(1)
	updateID := ""
	if update != nil {
		updateID = update.UpdateId
	}
	return &dtv1.ConfigUpdateResponse{
		Success:      false,
		ErrorMessage: fmt.Sprintf("%s: configuration hot reload is not enabled in P0", dterrors.CodeConfigNotEnabled),
		UpdateId:     updateID,
	}
}

func (r *Runtime) ListDevices(_ *dtv1.ListDevicesRequest) *dtv1.ListDevicesResponse {
	return &dtv1.ListDevicesResponse{}
}

func (r *Runtime) MetricsResponse() *dtv1.MetricsResponse {
	snapshot := r.Snapshot()
	return &dtv1.MetricsResponse{
		Timestamp:           snapshot.Timestamp,
		ConnectedDevices:    snapshot.ConnectedDevices,
		ActiveConnectors:    snapshot.ActiveConnectors,
		UpstreamMsgPerSec:   float64(snapshot.UpstreamTotal),
		DownstreamCmdPerSec: float64(snapshot.DownstreamTotal),
		BufferSize:          snapshot.BufferSize,
		BufferUsagePercent:  snapshot.BufferUsagePercent,
		BackpressureActive:  false,
		BackpressurePolicy:  dtv1.BackpressurePolicy_BP_UNSPECIFIED,
		QueueUsagePercent:   snapshot.BufferUsagePercent,
		Connectors:          map[string]*dtv1.ConnectorMetrics{},
	}
}

func (r *Runtime) Snapshot() Snapshot {
	r.mu.RLock()
	grpcServing := r.grpcServing
	mqttConnected := r.mqttConnected
	r.mu.RUnlock()

	bufferSize, bufferUsage := r.store.Stats()
	return Snapshot{
		Timestamp:             nowMillis(),
		Ready:                 r.ready(grpcServing, mqttConnected),
		GRPCServing:           grpcServing,
		MQTTConnected:         mqttConnected,
		BufferSize:            int64(bufferSize),
		BufferUsagePercent:    bufferUsage,
		ConnectedDevices:      0,
		ActiveConnectors:      0,
		UpstreamTotal:         r.upstreamTotal.Load(),
		DownstreamTotal:       r.downstreamTotal.Load(),
		RejectedCommandTotal:  r.rejectedCommandTotal.Load(),
		DuplicateCommandTotal: r.duplicateCommandTotal.Load(),
		ConfigRejectTotal:     r.configRejectTotal.Load(),
	}
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
	switch r.cfg.RunMode {
	case config.RunModeSplit:
		return mqttConnected
	default:
		return grpcServing
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
			"phase": "P0",
		},
	}
}

func IsDuplicateCommand(err error) bool {
	return errors.Is(err, command.ErrDuplicate)
}

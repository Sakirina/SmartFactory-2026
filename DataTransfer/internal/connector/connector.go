package connector

import (
	"context"
	"fmt"
	"sync"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
)

const (
	StateInitializing = "initializing"
	StateRunning      = "running"
	StateError        = "error"
	StateStopped      = "stopped"
)

type Connector interface {
	Init(config config.ConnectorConfig) error
	Start(ctx context.Context, upstream chan<- *dtv1.DeviceMessage) error
	SendCommand(ctx context.Context, cmd *dtv1.DeviceMessage) (*dtv1.CommandResponsePayload, error)
	Stop() error
	Status() Status
	Devices() []dtv1.DeviceInfo
	ReloadConfig(config config.ConnectorConfig) error
}

type Factory func() Connector

type Publisher interface {
	Publish(msg *dtv1.DeviceMessage) error
}

type Status struct {
	ConnectorID  string
	Protocol     string
	State        string
	DeviceCount  int
	ErrorMessage string
	Uptime       int64
	Stats        Stats
}

type Stats struct {
	MessagesIn   int64
	MessagesOut  int64
	ErrorsTotal  int64
	AvgLatencyMs float64
}

var registry = struct {
	sync.RWMutex
	factories map[string]Factory
}{
	factories: make(map[string]Factory),
}

func Register(protocol string, factory Factory) {
	registry.Lock()
	defer registry.Unlock()
	registry.factories[protocol] = factory
}

func factoryFor(protocol string) (Factory, bool) {
	registry.RLock()
	defer registry.RUnlock()
	factory, ok := registry.factories[protocol]
	return factory, ok
}

func NewStatus(connectorID, protocol string) Status {
	return Status{
		ConnectorID: connectorID,
		Protocol:    protocol,
		State:       StateInitializing,
	}
}

func ErrorStatus(connectorID, protocol string, err error) Status {
	status := NewStatus(connectorID, protocol)
	status.State = StateError
	if err != nil {
		status.ErrorMessage = err.Error()
	}
	return status
}

func StatusMetrics(status Status) *dtv1.ConnectorMetrics {
	return &dtv1.ConnectorMetrics{
		ConnectorId:  status.ConnectorID,
		Protocol:     status.Protocol,
		State:        status.State,
		DeviceCount:  int32(status.DeviceCount),
		MsgPerSec:    float64(status.Stats.MessagesIn),
		AvgLatencyMs: status.Stats.AvgLatencyMs,
		ErrorCount:   status.Stats.ErrorsTotal,
	}
}

func DeviceInfoFromConfig(connectorID, protocol string, tags map[string]string, device config.DeviceConfig, state dtv1.DeviceState, now time.Time) dtv1.DeviceInfo {
	return dtv1.DeviceInfo{
		Identity: &dtv1.DeviceIdentity{
			DeviceId:    device.DeviceID,
			DeviceName:  device.DeviceName,
			DeviceType:  device.DeviceType,
			ConnectorId: connectorID,
			Protocol:    protocol,
			Tags:        tags,
		},
		State:       state,
		ConnectedAt: now.UnixMilli(),
		LastSeen:    now.UnixMilli(),
	}
}

func UnknownProtocolError(protocol string) error {
	return fmt.Errorf("connector protocol %q is not registered", protocol)
}

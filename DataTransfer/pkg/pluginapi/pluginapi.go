package pluginapi

import (
	"context"
	"encoding/json"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
)

type DeviceMessage = dtv1.DeviceMessage
type CommandResponsePayload = dtv1.CommandResponsePayload
type ReportStrategyConfig = dtv1.ReportStrategyConfig
type DeviceInfo = dtv1.DeviceInfo

const (
	ConnectorStateInitializing = "initializing"
	ConnectorStateRunning      = "running"
	ConnectorStateError        = "error"
	ConnectorStateStopped      = "stopped"

	ProtocolModbusTCP = "modbus_tcp"
)

type Connector interface {
	Init(config ConnectorConfig) error
	Start(ctx context.Context, upstream chan<- *DeviceMessage) error
	SendCommand(ctx context.Context, cmd *DeviceMessage) (*CommandResponsePayload, error)
	Stop() error
	Status() ConnectorStatus
	Devices() []DeviceInfo
	ReloadConfig(config ConnectorConfig) error
}

type Converter interface {
	ConvertUpstream(rawData []byte, metadata DeviceMetadata) (*DeviceMessage, error)
	ConvertDownstream(cmd *DeviceMessage, metadata DeviceMetadata) ([]byte, error)
}

type ConnectorFactory func() Connector

type ConnectorConfig struct {
	ConnectorID     string
	Protocol        string
	DefaultTags     map[string]string
	Connection      json.RawMessage
	Devices         []DeviceConfig
	Polling         *PollingConfig
	ConverterConfig json.RawMessage
	ReportStrategy  *ReportStrategyConfig
}

type DeviceConfig struct {
	DeviceID   string
	DeviceName string
	DeviceType string
	Tags       map[string]string
	Address    json.RawMessage
	Datapoints []DatapointConfig
}

type DatapointConfig struct {
	Key       string
	Source    string
	Transform string
	Unit      string
	Quality   string
	Strategy  *ReportStrategyConfig
}

type PollingConfig struct {
	IntervalMillis int
	TimeoutMillis  int
}

type DeviceMetadata struct {
	DeviceID    string
	ConnectorID string
	Protocol    string
	Timestamp   int64
	Tags        map[string]string
}

type ConnectorStatus struct {
	State        string
	DeviceCount  int
	ErrorMessage string
	Uptime       int64
	Stats        ConnectorStats
}

type ConnectorStats struct {
	MessagesIn   int64
	MessagesOut  int64
	ErrorsTotal  int64
	AvgLatencyMs float64
}

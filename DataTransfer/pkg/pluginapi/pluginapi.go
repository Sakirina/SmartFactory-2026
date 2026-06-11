// Package pluginapi 暴露 Connector 插件开发契约(接口文档 4.1):
// 插件只依赖本包与生成的 proto 类型,不得 import internal/。
// 与 internal/connector 的接口保持同构;sidecar 进程间隔离见 plugin proto。
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
type ConfigUpdateResponse = dtv1.ConfigUpdateResponse

const (
	ConnectorStateInitializing = "initializing"
	ConnectorStateRunning      = "running"
	ConnectorStateError        = "error"
	ConnectorStateStopped      = "stopped"

	ProtocolModbusTCP  = "modbus_tcp"
	ProtocolMQTTDevice = "mqtt_device"
	ProtocolOPCUA      = "opcua"
	ProtocolSidecar    = "sidecar"
)

type Connector interface {
	Init(config ConnectorConfig) error
	Start(ctx context.Context, upstream chan<- *DeviceMessage) error
	SendCommand(ctx context.Context, cmd *DeviceMessage) (*CommandResponsePayload, error)
	Stop() error
	Status() ConnectorStatus
	// Devices 返回设备快照。元素为独立副本(指针切片避免按值拷贝 protobuf 消息)。
	Devices() []*DeviceInfo
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

// Package config 定义服务配置模型与加载链:默认值 → YAML → 环境变量 → 校验。
// 结构体的 json 标签同时是设备注册微服务配置推送内嵌 JSON 的跨服务契约(CSC-002),
// 字段名与 YAML 保持 snake_case 一致,不得随意改动。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	RunModeEmbedded = "embedded"
	RunModeSplit    = "split"

	// Environment 取值。默认 production:生产语义更严格(如禁止 gRPC 反射),
	// 漏配环境变量时不会意外暴露调试面。
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

type Config struct {
	// Environment 区分开发/生产行为开关(目前控制 gRPC 反射;后续调试面均挂此开关)。
	Environment string            `yaml:"environment"`
	RunMode     string            `yaml:"run_mode"`
	Log         LogConfig         `yaml:"log"`
	Management  ManagementConfig  `yaml:"management"`
	GRPC        GRPCConfig        `yaml:"grpc"`
	MQTT        MQTTConfig        `yaml:"mqtt"`
	Buffer      BufferConfig      `yaml:"buffer"`
	Runtime     RuntimeConfig     `yaml:"runtime"`
	Connectors  []ConnectorConfig `yaml:"connectors"`
	// ReportStrategy 为全局默认上报策略(FR-S-035 最高层级兜底,默认 ON_RECEIVED 即关闭过滤)。
	ReportStrategy ReportStrategyConfig `yaml:"report_strategy"`
	// Backpressure 为全局背压策略(FR-S-038),默认 BP_BLOCK。
	Backpressure BackpressureConfig `yaml:"backpressure"`
}

type BackpressureConfig struct {
	Policy string `yaml:"policy"` // BP_BLOCK | BP_DROP_OLDEST | BP_DEGRADE
}

type LogConfig struct {
	Level string `yaml:"level"`
}

type ManagementConfig struct {
	Addr string `yaml:"addr"`
}

type GRPCConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
	// Reflection 注册 gRPC Server Reflection(grpcurl 等调试工具依赖)。
	// 仅 environment=development 允许开启,默认关闭;production 配置 true 会被校验拒绝。
	Reflection bool `yaml:"reflection"`
	// TLS 为 gRPC 服务端加密(接口文档 3.1.3"可选开启 mTLS"的落点):
	// cert_file/key_file 必填;ca_file 配置后启用 mTLS 双向认证。
	// 开发环境下反射同样工作在 TLS 之上。
	TLS TLSConfig `yaml:"tls"`
}

type MQTTConfig struct {
	Enabled        bool      `yaml:"enabled"`
	Broker         string    `yaml:"broker"`
	GatewayID      string    `yaml:"gateway_id"`
	ClientID       string    `yaml:"client_id"`
	Username       string    `yaml:"username"`
	Password       string    `yaml:"password"`
	ConnectTimeout int       `yaml:"connect_timeout_seconds"`
	TLS            TLSConfig `yaml:"tls"`
}

type TLSConfig struct {
	Enabled            bool   `yaml:"enabled"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
	CertFile           string `yaml:"cert_file"`
	KeyFile            string `yaml:"key_file"`
	CAFile             string `yaml:"ca_file"`
}

type RuntimeConfig struct {
	RingSize          int `yaml:"ring_size"`
	CommandTTLSeconds int `yaml:"command_ttl_seconds"`
}

type BufferConfig struct {
	Enabled                bool   `yaml:"enabled"`
	StorageType            string `yaml:"storage_type"`
	Path                   string `yaml:"path"`
	MaxSizeMB              int    `yaml:"max_size_mb"`
	TTLHours               int    `yaml:"ttl_hours"`
	ResumeRateLimit        int    `yaml:"resume_rate_limit"`
	ResumeBatchSize        int    `yaml:"resume_batch_size"`
	CleanupIntervalSeconds int    `yaml:"cleanup_interval_seconds"`
}

type ConnectorConfig struct {
	ConnectorID    string                   `yaml:"connector_id"`
	Protocol       string                   `yaml:"protocol"`
	DefaultTags    map[string]string        `yaml:"default_tags"`
	Connection     ConnectionConfig         `yaml:"connection"`
	Polling        PollingConfig            `yaml:"polling"`
	Devices        []DeviceConfig           `yaml:"devices"`
	ActionMappings map[string]ActionMapping `yaml:"action_mappings"`
	ReportStrategy ReportStrategyConfig     `yaml:"report_strategy"`
}

// 以下结构同时承载 YAML(本地配置)与 JSON(设备注册微服务推送的
// connection/polling/datapoints 字节字段)两种编码。json 标签即跨服务 JSON 契约,
// 与 YAML 字段名保持一致(snake_case),已在 CSC-002 中向设备注册微服务声明。

type ConnectionConfig struct {
	URL                  string    `yaml:"url" json:"url"`
	Host                 string    `yaml:"host" json:"host"`
	Port                 int       `yaml:"port" json:"port"`
	UnitID               uint8     `yaml:"unit_id" json:"unit_id"`
	TimeoutMillis        int       `yaml:"timeout_millis" json:"timeout_millis"`
	Username             string    `yaml:"username" json:"username"`
	Password             string    `yaml:"password" json:"password"`
	SecurityPolicy       string    `yaml:"security_policy" json:"security_policy"`
	SecurityMode         string    `yaml:"security_mode" json:"security_mode"`
	CertFile             string    `yaml:"cert_file" json:"cert_file"`
	KeyFile              string    `yaml:"key_file" json:"key_file"`
	CAFile               string    `yaml:"ca_file" json:"ca_file"`
	TelemetryTopic       string    `yaml:"telemetry_topic" json:"telemetry_topic"`
	StatusTopic          string    `yaml:"status_topic" json:"status_topic"`
	EventTopic           string    `yaml:"event_topic" json:"event_topic"`
	CommandResponseTopic string    `yaml:"cmd_response_topic" json:"cmd_response_topic"`
	CommandTopic         string    `yaml:"command_topic" json:"command_topic"`
	TLS                  TLSConfig `yaml:"tls" json:"tls"`
}

type PollingConfig struct {
	IntervalMillis int `yaml:"interval_millis" json:"interval_millis"`
	TimeoutMillis  int `yaml:"timeout_millis" json:"timeout_millis"`
}

type DeviceConfig struct {
	DeviceID       string                   `yaml:"device_id" json:"device_id"`
	DeviceName     string                   `yaml:"device_name" json:"device_name"`
	DeviceType     string                   `yaml:"device_type" json:"device_type"`
	Protocol       string                   `yaml:"protocol" json:"protocol"`
	UnitID         uint8                    `yaml:"unit_id" json:"unit_id"`
	Tags           map[string]string        `yaml:"tags" json:"tags"`
	Address        json.RawMessage          `yaml:"address" json:"address,omitempty"`
	Datapoints     []DatapointConfig        `yaml:"datapoints" json:"datapoints"`
	ActionMappings map[string]ActionMapping `yaml:"action_mappings" json:"action_mappings,omitempty"`
}

type DatapointConfig struct {
	Key          string   `yaml:"key" json:"key"`
	Source       string   `yaml:"source" json:"source,omitempty"`
	NodeID       string   `yaml:"node_id" json:"node_id,omitempty"`
	RegisterType string   `yaml:"register_type" json:"register_type,omitempty"`
	Address      uint16   `yaml:"address" json:"address,omitempty"`
	Quantity     uint16   `yaml:"quantity" json:"quantity,omitempty"`
	DataType     string   `yaml:"data_type" json:"data_type,omitempty"`
	Scale        *float64 `yaml:"scale" json:"scale,omitempty"`
	Offset       float64  `yaml:"offset" json:"offset,omitempty"`
	Unit         string   `yaml:"unit" json:"unit,omitempty"`
	Quality      string   `yaml:"quality" json:"quality,omitempty"`
	// Strategy 为数据点级上报策略覆盖(FR-S-035 最低层级,优先级最高)。
	Strategy *ReportStrategyConfig `yaml:"report_strategy" json:"report_strategy,omitempty"`
}

type ActionMapping struct {
	Type         string   `yaml:"type" json:"type"`
	RegisterType string   `yaml:"register_type" json:"register_type,omitempty"`
	Address      uint16   `yaml:"address" json:"address,omitempty"`
	NodeID       string   `yaml:"node_id" json:"node_id,omitempty"`
	MethodID     string   `yaml:"method_id" json:"method_id,omitempty"`
	Quantity     uint16   `yaml:"quantity" json:"quantity,omitempty"`
	DataType     string   `yaml:"data_type" json:"data_type,omitempty"`
	Param        string   `yaml:"param" json:"param,omitempty"`
	Value        string   `yaml:"value" json:"value,omitempty"`
	Values       []string `yaml:"values" json:"values,omitempty"`
	Topic        string   `yaml:"topic" json:"topic,omitempty"`
	Template     string   `yaml:"template" json:"template,omitempty"`
}

type ReportStrategyConfig struct {
	Mode          string  `yaml:"mode" json:"mode"`
	PeriodSeconds int     `yaml:"period_seconds" json:"period_seconds,omitempty"`
	Deadband      float64 `yaml:"deadband" json:"deadband,omitempty"`
}

func Defaults() Config {
	return Config{
		Environment: EnvProduction,
		RunMode:     RunModeEmbedded,
		Log: LogConfig{
			Level: "info",
		},
		Management: ManagementConfig{
			Addr: ":8080",
		},
		GRPC: GRPCConfig{
			Enabled: true,
			Addr:    "127.0.0.1:50051",
		},
		MQTT: MQTTConfig{
			Enabled:        false,
			ConnectTimeout: 5,
		},
		Buffer: BufferConfig{
			Enabled:                false,
			StorageType:            "sqlite",
			Path:                   "data/datatransfer-buffer.db",
			MaxSizeMB:              512,
			TTLHours:               168,
			ResumeRateLimit:        1000,
			ResumeBatchSize:        100,
			CleanupIntervalSeconds: 60,
		},
		Runtime: RuntimeConfig{
			RingSize:          1024,
			CommandTTLSeconds: int(time.Hour.Seconds()),
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, err
		}
	}
	applyEnv(&cfg, os.LookupEnv)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	c.Environment = strings.ToLower(strings.TrimSpace(c.Environment))
	if c.Environment == "" {
		c.Environment = EnvProduction
	}
	if c.Environment != EnvDevelopment && c.Environment != EnvProduction {
		return fmt.Errorf("environment must be %q or %q", EnvDevelopment, EnvProduction)
	}
	c.RunMode = strings.TrimSpace(c.RunMode)
	if c.RunMode == "" {
		c.RunMode = RunModeEmbedded
	}
	if c.RunMode != RunModeEmbedded && c.RunMode != RunModeSplit {
		return fmt.Errorf("run_mode must be %q or %q", RunModeEmbedded, RunModeSplit)
	}
	// 反射是调试面,生产环境直接拒绝启动(fail-fast),避免误配置带上线。
	if c.GRPC.Reflection && c.Environment != EnvDevelopment {
		return fmt.Errorf("grpc.reflection is only allowed when environment is %q", EnvDevelopment)
	}
	if c.GRPC.TLS.Enabled && (strings.TrimSpace(c.GRPC.TLS.CertFile) == "" || strings.TrimSpace(c.GRPC.TLS.KeyFile) == "") {
		return errors.New("grpc.tls requires cert_file and key_file when enabled")
	}
	if strings.TrimSpace(c.Management.Addr) == "" {
		return errors.New("management.addr is required")
	}
	if c.GRPC.Enabled && strings.TrimSpace(c.GRPC.Addr) == "" {
		return errors.New("grpc.addr is required when grpc.enabled is true")
	}
	if c.Runtime.RingSize <= 0 {
		c.Runtime.RingSize = Defaults().Runtime.RingSize
	}
	if c.Runtime.CommandTTLSeconds <= 0 {
		c.Runtime.CommandTTLSeconds = Defaults().Runtime.CommandTTLSeconds
	}
	if c.RunMode == RunModeSplit {
		c.MQTT.Enabled = true
		c.Buffer.Enabled = true
	}
	if err := normalizeStrategy(&c.ReportStrategy, "report_strategy"); err != nil {
		return err
	}
	c.Backpressure.Policy = strings.ToUpper(strings.TrimSpace(c.Backpressure.Policy))
	switch c.Backpressure.Policy {
	case "", "BP_BLOCK", "BP_DROP_OLDEST", "BP_DEGRADE":
	default:
		return fmt.Errorf("backpressure.policy %q is invalid (BP_BLOCK | BP_DROP_OLDEST | BP_DEGRADE)", c.Backpressure.Policy)
	}
	if c.MQTT.Enabled {
		if strings.TrimSpace(c.MQTT.Broker) == "" {
			return errors.New("mqtt.broker is required when mqtt.enabled is true")
		}
		if strings.TrimSpace(c.MQTT.GatewayID) == "" {
			return errors.New("mqtt.gateway_id is required when mqtt.enabled is true")
		}
		if strings.TrimSpace(c.MQTT.ClientID) == "" {
			c.MQTT.ClientID = "gateway-" + c.MQTT.GatewayID
		}
		if c.MQTT.ConnectTimeout <= 0 {
			c.MQTT.ConnectTimeout = Defaults().MQTT.ConnectTimeout
		}
	}
	if c.Buffer.Enabled {
		c.Buffer.StorageType = strings.ToLower(strings.TrimSpace(c.Buffer.StorageType))
		if c.Buffer.StorageType == "" {
			c.Buffer.StorageType = Defaults().Buffer.StorageType
		}
		if c.Buffer.StorageType != "sqlite" {
			return fmt.Errorf("buffer.storage_type %q is not supported in P2; use %q", c.Buffer.StorageType, "sqlite")
		}
		if strings.TrimSpace(c.Buffer.Path) == "" {
			c.Buffer.Path = Defaults().Buffer.Path
		}
		if c.Buffer.MaxSizeMB <= 0 {
			c.Buffer.MaxSizeMB = Defaults().Buffer.MaxSizeMB
		}
		if c.Buffer.TTLHours <= 0 {
			c.Buffer.TTLHours = Defaults().Buffer.TTLHours
		}
		if c.Buffer.ResumeRateLimit <= 0 {
			c.Buffer.ResumeRateLimit = Defaults().Buffer.ResumeRateLimit
		}
		if c.Buffer.ResumeBatchSize <= 0 {
			c.Buffer.ResumeBatchSize = Defaults().Buffer.ResumeBatchSize
		}
		if c.Buffer.CleanupIntervalSeconds <= 0 {
			c.Buffer.CleanupIntervalSeconds = Defaults().Buffer.CleanupIntervalSeconds
		}
	}
	seenConnectors := make(map[string]struct{}, len(c.Connectors))
	seenDevices := make(map[string]struct{})
	for idx := range c.Connectors {
		conn := &c.Connectors[idx]
		conn.ConnectorID = strings.TrimSpace(conn.ConnectorID)
		conn.Protocol = strings.ToLower(strings.TrimSpace(conn.Protocol))
		if conn.ConnectorID == "" {
			return fmt.Errorf("connectors[%d].connector_id is required", idx)
		}
		if conn.Protocol == "" {
			return fmt.Errorf("connectors[%d].protocol is required", idx)
		}
		if _, ok := seenConnectors[conn.ConnectorID]; ok {
			return fmt.Errorf("duplicate connector_id %q", conn.ConnectorID)
		}
		seenConnectors[conn.ConnectorID] = struct{}{}
		if conn.Connection.TimeoutMillis <= 0 {
			conn.Connection.TimeoutMillis = 1000
		}
		if conn.Polling.IntervalMillis <= 0 && conn.Protocol == "modbus_tcp" {
			conn.Polling.IntervalMillis = 1000
		}
		if conn.Polling.TimeoutMillis <= 0 {
			conn.Polling.TimeoutMillis = conn.Connection.TimeoutMillis
		}
		for devIdx := range conn.Devices {
			device := &conn.Devices[devIdx]
			device.DeviceID = strings.TrimSpace(device.DeviceID)
			if device.DeviceID == "" {
				return fmt.Errorf("connectors[%d].devices[%d].device_id is required", idx, devIdx)
			}
			if _, ok := seenDevices[device.DeviceID]; ok {
				return fmt.Errorf("duplicate device_id %q", device.DeviceID)
			}
			seenDevices[device.DeviceID] = struct{}{}
			if device.Protocol == "" {
				device.Protocol = conn.Protocol
			} else {
				device.Protocol = strings.ToLower(strings.TrimSpace(device.Protocol))
			}
			for dpIdx := range device.Datapoints {
				dp := &device.Datapoints[dpIdx]
				dp.Key = strings.TrimSpace(dp.Key)
				dp.Source = strings.TrimSpace(dp.Source)
				dp.NodeID = strings.TrimSpace(dp.NodeID)
				dp.RegisterType = strings.ToLower(strings.TrimSpace(dp.RegisterType))
				dp.DataType = strings.ToLower(strings.TrimSpace(dp.DataType))
				if dp.Key == "" {
					return fmt.Errorf("connectors[%d].devices[%d].datapoints[%d].key is required", idx, devIdx, dpIdx)
				}
				switch device.Protocol {
				case "modbus_tcp":
					if dp.RegisterType == "" {
						return fmt.Errorf("connectors[%d].devices[%d].datapoints[%d].register_type is required", idx, devIdx, dpIdx)
					}
				case "mqtt_device":
					if dp.Source == "" {
						return fmt.Errorf("connectors[%d].devices[%d].datapoints[%d].source is required", idx, devIdx, dpIdx)
					}
				case "opcua":
					if dp.NodeID == "" {
						return fmt.Errorf("connectors[%d].devices[%d].datapoints[%d].node_id is required", idx, devIdx, dpIdx)
					}
				}
				if dp.DataType == "" {
					dp.DataType = "uint16"
				}
				if dp.Strategy != nil {
					if err := normalizeStrategy(dp.Strategy, fmt.Sprintf("connectors[%d].devices[%d].datapoints[%d].report_strategy", idx, devIdx, dpIdx)); err != nil {
						return err
					}
				}
			}
		}
		if err := normalizeStrategy(&conn.ReportStrategy, fmt.Sprintf("connectors[%d].report_strategy", idx)); err != nil {
			return err
		}
	}
	return nil
}

// normalizeStrategy 规范化上报策略模式并校验周期参数(FR-S-034:周期模式必须给定周期)。
func normalizeStrategy(strategy *ReportStrategyConfig, path string) error {
	if strategy == nil {
		return nil
	}
	strategy.Mode = strings.ToUpper(strings.TrimSpace(strategy.Mode))
	switch strategy.Mode {
	case "", "STRATEGY_UNSPECIFIED", "ON_RECEIVED", "ON_CHANGE":
	case "ON_REPORT_PERIOD", "ON_CHANGE_OR_REPORT_PERIOD":
		if strategy.PeriodSeconds <= 0 {
			return fmt.Errorf("%s.period_seconds must be > 0 for mode %s", path, strategy.Mode)
		}
	default:
		return fmt.Errorf("%s.mode %q is invalid", path, strategy.Mode)
	}
	if strategy.Deadband < 0 {
		return fmt.Errorf("%s.deadband must be >= 0", path)
	}
	return nil
}

func applyEnv(cfg *Config, lookup func(string) (string, bool)) {
	setString(lookup, "DT_ENVIRONMENT", &cfg.Environment)
	setString(lookup, "DT_RUN_MODE", &cfg.RunMode)
	setString(lookup, "DT_LOG_LEVEL", &cfg.Log.Level)
	setString(lookup, "DT_MANAGEMENT_ADDR", &cfg.Management.Addr)
	setBool(lookup, "DT_GRPC_ENABLED", &cfg.GRPC.Enabled)
	setString(lookup, "DT_GRPC_ADDR", &cfg.GRPC.Addr)
	setBool(lookup, "DT_GRPC_REFLECTION", &cfg.GRPC.Reflection)
	setBool(lookup, "DT_GRPC_TLS_ENABLED", &cfg.GRPC.TLS.Enabled)
	setString(lookup, "DT_GRPC_TLS_CERT_FILE", &cfg.GRPC.TLS.CertFile)
	setString(lookup, "DT_GRPC_TLS_KEY_FILE", &cfg.GRPC.TLS.KeyFile)
	setString(lookup, "DT_GRPC_TLS_CA_FILE", &cfg.GRPC.TLS.CAFile)
	setBool(lookup, "DT_MQTT_ENABLED", &cfg.MQTT.Enabled)
	setString(lookup, "DT_MQTT_BROKER", &cfg.MQTT.Broker)
	setString(lookup, "DT_MQTT_GATEWAY_ID", &cfg.MQTT.GatewayID)
	setString(lookup, "DT_MQTT_CLIENT_ID", &cfg.MQTT.ClientID)
	setString(lookup, "DT_MQTT_USERNAME", &cfg.MQTT.Username)
	setString(lookup, "DT_MQTT_PASSWORD", &cfg.MQTT.Password)
	setBool(lookup, "DT_MQTT_TLS_ENABLED", &cfg.MQTT.TLS.Enabled)
	setBool(lookup, "DT_MQTT_TLS_INSECURE_SKIP_VERIFY", &cfg.MQTT.TLS.InsecureSkipVerify)
	setString(lookup, "DT_MQTT_TLS_CERT_FILE", &cfg.MQTT.TLS.CertFile)
	setString(lookup, "DT_MQTT_TLS_KEY_FILE", &cfg.MQTT.TLS.KeyFile)
	setString(lookup, "DT_MQTT_TLS_CA_FILE", &cfg.MQTT.TLS.CAFile)
	setInt(lookup, "DT_MQTT_CONNECT_TIMEOUT_SECONDS", &cfg.MQTT.ConnectTimeout)
	setString(lookup, "DT_BACKPRESSURE_POLICY", &cfg.Backpressure.Policy)
	setBool(lookup, "DT_BUFFER_ENABLED", &cfg.Buffer.Enabled)
	setString(lookup, "DT_BUFFER_STORAGE_TYPE", &cfg.Buffer.StorageType)
	setString(lookup, "DT_BUFFER_PATH", &cfg.Buffer.Path)
	setInt(lookup, "DT_BUFFER_MAX_SIZE_MB", &cfg.Buffer.MaxSizeMB)
	setInt(lookup, "DT_BUFFER_TTL_HOURS", &cfg.Buffer.TTLHours)
	setInt(lookup, "DT_BUFFER_RESUME_RATE_LIMIT", &cfg.Buffer.ResumeRateLimit)
	setInt(lookup, "DT_BUFFER_RESUME_BATCH_SIZE", &cfg.Buffer.ResumeBatchSize)
	setInt(lookup, "DT_BUFFER_CLEANUP_INTERVAL_SECONDS", &cfg.Buffer.CleanupIntervalSeconds)
	setInt(lookup, "DT_RUNTIME_RING_SIZE", &cfg.Runtime.RingSize)
	setInt(lookup, "DT_RUNTIME_COMMAND_TTL_SECONDS", &cfg.Runtime.CommandTTLSeconds)
}

func setString(lookup func(string) (string, bool), key string, target *string) {
	if value, ok := lookup(key); ok {
		*target = value
	}
}

func setBool(lookup func(string) (string, bool), key string, target *bool) {
	if value, ok := lookup(key); ok {
		parsed, err := strconv.ParseBool(value)
		if err == nil {
			*target = parsed
		}
	}
}

func setInt(lookup func(string) (string, bool), key string, target *int) {
	if value, ok := lookup(key); ok {
		parsed, err := strconv.Atoi(value)
		if err == nil {
			*target = parsed
		}
	}
}

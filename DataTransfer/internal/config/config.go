package config

import (
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
)

type Config struct {
	RunMode    string            `yaml:"run_mode"`
	Log        LogConfig         `yaml:"log"`
	Management ManagementConfig  `yaml:"management"`
	GRPC       GRPCConfig        `yaml:"grpc"`
	MQTT       MQTTConfig        `yaml:"mqtt"`
	Runtime    RuntimeConfig     `yaml:"runtime"`
	Connectors []ConnectorConfig `yaml:"connectors"`
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
	Enabled            bool `yaml:"enabled"`
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

type RuntimeConfig struct {
	RingSize          int `yaml:"ring_size"`
	CommandTTLSeconds int `yaml:"command_ttl_seconds"`
}

type ConnectorConfig struct {
	ConnectorID    string                   `yaml:"connector_id"`
	Protocol       string                   `yaml:"protocol"`
	DefaultTags    map[string]string        `yaml:"default_tags"`
	Connection     ConnectionConfig         `yaml:"connection"`
	Polling        PollingConfig            `yaml:"polling"`
	Devices        []DeviceConfig           `yaml:"devices"`
	ActionMappings map[string]ActionMapping `yaml:"action_mappings"`
}

type ConnectionConfig struct {
	URL           string `yaml:"url"`
	Host          string `yaml:"host"`
	Port          int    `yaml:"port"`
	UnitID        uint8  `yaml:"unit_id"`
	TimeoutMillis int    `yaml:"timeout_millis"`
}

type PollingConfig struct {
	IntervalMillis int `yaml:"interval_millis"`
	TimeoutMillis  int `yaml:"timeout_millis"`
}

type DeviceConfig struct {
	DeviceID       string                   `yaml:"device_id"`
	DeviceName     string                   `yaml:"device_name"`
	DeviceType     string                   `yaml:"device_type"`
	Protocol       string                   `yaml:"protocol"`
	UnitID         uint8                    `yaml:"unit_id"`
	Tags           map[string]string        `yaml:"tags"`
	Datapoints     []DatapointConfig        `yaml:"datapoints"`
	ActionMappings map[string]ActionMapping `yaml:"action_mappings"`
}

type DatapointConfig struct {
	Key          string   `yaml:"key"`
	RegisterType string   `yaml:"register_type"`
	Address      uint16   `yaml:"address"`
	Quantity     uint16   `yaml:"quantity"`
	DataType     string   `yaml:"data_type"`
	Scale        *float64 `yaml:"scale"`
	Offset       float64  `yaml:"offset"`
	Unit         string   `yaml:"unit"`
	Quality      string   `yaml:"quality"`
}

type ActionMapping struct {
	Type         string   `yaml:"type"`
	RegisterType string   `yaml:"register_type"`
	Address      uint16   `yaml:"address"`
	Quantity     uint16   `yaml:"quantity"`
	DataType     string   `yaml:"data_type"`
	Param        string   `yaml:"param"`
	Value        string   `yaml:"value"`
	Values       []string `yaml:"values"`
}

func Defaults() Config {
	return Config{
		RunMode: RunModeEmbedded,
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
	c.RunMode = strings.TrimSpace(c.RunMode)
	if c.RunMode == "" {
		c.RunMode = RunModeEmbedded
	}
	if c.RunMode != RunModeEmbedded && c.RunMode != RunModeSplit {
		return fmt.Errorf("run_mode must be %q or %q", RunModeEmbedded, RunModeSplit)
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
		if conn.Polling.IntervalMillis <= 0 {
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
				dp.RegisterType = strings.ToLower(strings.TrimSpace(dp.RegisterType))
				dp.DataType = strings.ToLower(strings.TrimSpace(dp.DataType))
				if dp.Key == "" {
					return fmt.Errorf("connectors[%d].devices[%d].datapoints[%d].key is required", idx, devIdx, dpIdx)
				}
				if dp.RegisterType == "" {
					return fmt.Errorf("connectors[%d].devices[%d].datapoints[%d].register_type is required", idx, devIdx, dpIdx)
				}
				if dp.DataType == "" {
					dp.DataType = "uint16"
				}
			}
		}
	}
	return nil
}

func applyEnv(cfg *Config, lookup func(string) (string, bool)) {
	setString(lookup, "DT_RUN_MODE", &cfg.RunMode)
	setString(lookup, "DT_LOG_LEVEL", &cfg.Log.Level)
	setString(lookup, "DT_MANAGEMENT_ADDR", &cfg.Management.Addr)
	setBool(lookup, "DT_GRPC_ENABLED", &cfg.GRPC.Enabled)
	setString(lookup, "DT_GRPC_ADDR", &cfg.GRPC.Addr)
	setBool(lookup, "DT_MQTT_ENABLED", &cfg.MQTT.Enabled)
	setString(lookup, "DT_MQTT_BROKER", &cfg.MQTT.Broker)
	setString(lookup, "DT_MQTT_GATEWAY_ID", &cfg.MQTT.GatewayID)
	setString(lookup, "DT_MQTT_CLIENT_ID", &cfg.MQTT.ClientID)
	setString(lookup, "DT_MQTT_USERNAME", &cfg.MQTT.Username)
	setString(lookup, "DT_MQTT_PASSWORD", &cfg.MQTT.Password)
	setBool(lookup, "DT_MQTT_TLS_ENABLED", &cfg.MQTT.TLS.Enabled)
	setBool(lookup, "DT_MQTT_TLS_INSECURE_SKIP_VERIFY", &cfg.MQTT.TLS.InsecureSkipVerify)
	setInt(lookup, "DT_MQTT_CONNECT_TIMEOUT_SECONDS", &cfg.MQTT.ConnectTimeout)
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

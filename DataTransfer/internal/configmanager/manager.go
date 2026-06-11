// Package configmanager 承接 DeviceConfigUpdate 配置热加载(设计 6.2/6.3):
// update_id 幂等去重、entity_revision 乱序保护(FR-S-029a)、设备与 Connector
// 的增删改差异应用,以及 UPDATE_GLOBAL 的全局策略热更。整个应用流程串行执行。
package configmanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	dterrors "competition2026/product/datatransfer/internal/errors"
	"google.golang.org/protobuf/proto"
)

// GlobalApplier 承接 UPDATE_GLOBAL 变更(全局上报策略、背压策略等),由 Runtime 实现。
type GlobalApplier interface {
	ApplyGlobalConfig(payload *dtv1.GlobalConfigPayload) error
}

type Manager struct {
	connectors *connector.Manager
	logger     *slog.Logger

	// mu 串行化整个 Apply 流程(幂等检查 + revision 比较 + 应用 + 记录)。
	// 配置推送是低频操作,串行化的代价可以接受;若拆开锁,
	// 两个并发推送可能同时通过 revision 检查,导致旧配置后到覆盖新配置,
	// 使 FR-S-029a 乱序保护失效。
	mu        sync.Mutex
	updates   map[string]*dtv1.ConfigUpdateResponse
	revisions map[string]int64
	global    GlobalApplier
}

func New(connectors *connector.Manager, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		connectors: connectors,
		logger:     logger,
		updates:    make(map[string]*dtv1.ConfigUpdateResponse),
		revisions:  make(map[string]int64),
	}
}

func (m *Manager) SetGlobalApplier(applier GlobalApplier) {
	m.mu.Lock()
	m.global = applier
	m.mu.Unlock()
}

func (m *Manager) Apply(update *dtv1.DeviceConfigUpdate) *dtv1.ConfigUpdateResponse {
	if update == nil {
		return failure("", "configuration update is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if update.GetUpdateId() == "" {
		return failure("", "update_id is required")
	}
	if existing, ok := m.updates[update.GetUpdateId()]; ok {
		return cloneResponse(existing)
	}
	entityKey, err := entityRevisionKey(update)
	if err != nil {
		return m.record(update, failure(update.GetUpdateId(), err.Error()), entityKey, false)
	}
	if revision := m.revisions[entityKey]; revision > 0 && update.GetEntityRevision() <= revision {
		m.logger.Warn("stale configuration update ignored",
			"update_id", update.GetUpdateId(), "entity", entityKey,
			"revision", update.GetEntityRevision(), "applied_revision", revision)
		return m.record(update, success(update.GetUpdateId(), "stale configuration update ignored"), entityKey, false)
	}

	applyErr := m.apply(update)
	if applyErr != nil {
		m.logger.Error("configuration update rejected",
			"code", dterrors.CodeConfigRejected,
			"update_id", update.GetUpdateId(), "entity", entityKey, "error", applyErr.Error())
		return m.record(update, failure(update.GetUpdateId(), applyErr.Error()), entityKey, false)
	}
	m.logger.Info("configuration update applied",
		"code", dterrors.CodeConfigApplied,
		"update_id", update.GetUpdateId(), "entity", entityKey,
		"revision", update.GetEntityRevision(), "source", update.GetChangeSource())
	return m.record(update, success(update.GetUpdateId(), ""), entityKey, true)
}

// record 登记响应与版本号(须在持有 m.mu 时调用),并返回响应副本。
func (m *Manager) record(update *dtv1.DeviceConfigUpdate, response *dtv1.ConfigUpdateResponse, entityKey string, applied bool) *dtv1.ConfigUpdateResponse {
	m.updates[update.GetUpdateId()] = response
	if applied && entityKey != "" {
		m.revisions[entityKey] = update.GetEntityRevision()
	}
	return cloneResponse(response)
}

func (m *Manager) apply(update *dtv1.DeviceConfigUpdate) error {
	if m.connectors == nil {
		return errors.New("connector manager is not attached")
	}
	switch update.GetAction() {
	case dtv1.DeviceConfigUpdate_ADD_DEVICE, dtv1.DeviceConfigUpdate_UPDATE_DEVICE:
		payload := update.GetDeviceConfig()
		device, err := deviceConfigFromPayload(payload, m.logger)
		if err != nil {
			return err
		}
		return m.connectors.ApplyDevice(payload.GetConnectorId(), device)
	case dtv1.DeviceConfigUpdate_REMOVE_DEVICE:
		payload := update.GetDeviceConfig()
		if payload == nil || payload.GetDeviceId() == "" || payload.GetConnectorId() == "" {
			return errors.New("remove device requires device_id and connector_id")
		}
		return m.connectors.RemoveDevice(payload.GetConnectorId(), payload.GetDeviceId())
	case dtv1.DeviceConfigUpdate_ADD_CONNECTOR, dtv1.DeviceConfigUpdate_UPDATE_CONNECTOR:
		connectorCfg, err := connectorConfigFromPayload(update.GetConnectorConfig(), m.connectors)
		if err != nil {
			return err
		}
		return m.connectors.ApplyConnector(connectorCfg)
	case dtv1.DeviceConfigUpdate_REMOVE_CONNECTOR:
		payload := update.GetConnectorConfig()
		if payload == nil || payload.GetConnectorId() == "" {
			return errors.New("remove connector requires connector_id")
		}
		return m.connectors.RemoveConnector(payload.GetConnectorId())
	case dtv1.DeviceConfigUpdate_UPDATE_GLOBAL:
		if m.global == nil {
			return errors.New("global config applier is not attached")
		}
		return m.global.ApplyGlobalConfig(update.GetGlobalConfig())
	default:
		return fmt.Errorf("unsupported config update action %s", update.GetAction().String())
	}
}

func entityRevisionKey(update *dtv1.DeviceConfigUpdate) (string, error) {
	switch update.GetAction() {
	case dtv1.DeviceConfigUpdate_ADD_DEVICE, dtv1.DeviceConfigUpdate_UPDATE_DEVICE, dtv1.DeviceConfigUpdate_REMOVE_DEVICE:
		payload := update.GetDeviceConfig()
		if payload == nil || payload.GetDeviceId() == "" {
			return "", errors.New("device config update requires device_id")
		}
		return "device:" + payload.GetDeviceId(), nil
	case dtv1.DeviceConfigUpdate_ADD_CONNECTOR, dtv1.DeviceConfigUpdate_UPDATE_CONNECTOR, dtv1.DeviceConfigUpdate_REMOVE_CONNECTOR:
		payload := update.GetConnectorConfig()
		if payload == nil || payload.GetConnectorId() == "" {
			return "", errors.New("connector config update requires connector_id")
		}
		return "connector:" + payload.GetConnectorId(), nil
	case dtv1.DeviceConfigUpdate_UPDATE_GLOBAL:
		return "global", nil
	default:
		return "", fmt.Errorf("unsupported config update action %s", update.GetAction().String())
	}
}

func deviceConfigFromPayload(payload *dtv1.DeviceConfigPayload, logger *slog.Logger) (config.DeviceConfig, error) {
	if payload == nil {
		return config.DeviceConfig{}, errors.New("device_config payload is required")
	}
	if payload.GetDeviceId() == "" || payload.GetConnectorId() == "" {
		return config.DeviceConfig{}, errors.New("device_config requires device_id and connector_id")
	}
	device := config.DeviceConfig{
		DeviceID:   payload.GetDeviceId(),
		DeviceName: payload.GetDeviceName(),
		DeviceType: payload.GetDeviceType(),
		Protocol:   "",
		Tags:       cloneStringMap(payload.GetTags()),
		Address:    cloneBytes(payload.GetAddress()),
		Datapoints: nil,
	}
	if len(device.Address) > 0 && !json.Valid(device.Address) {
		return config.DeviceConfig{}, errors.New("decode device address: invalid JSON")
	}
	if len(payload.GetDatapoints()) > 0 {
		if err := json.Unmarshal(payload.GetDatapoints(), &device.Datapoints); err != nil {
			return config.DeviceConfig{}, fmt.Errorf("decode device datapoints: %w", err)
		}
	}
	// 数据点级上报策略覆盖(FR-S-035 最低层级),按 key 匹配写入数据点配置。
	for _, override := range payload.GetStrategyOverrides() {
		strategy := override.GetStrategy()
		if override.GetKey() == "" || strategy == nil {
			continue
		}
		applied := false
		for idx := range device.Datapoints {
			if device.Datapoints[idx].Key == override.GetKey() {
				device.Datapoints[idx].Strategy = &config.ReportStrategyConfig{
					Mode:          strategy.GetMode().String(),
					PeriodSeconds: int(strategy.GetPeriodSeconds()),
					Deadband:      strategy.GetDeadband(),
				}
				applied = true
				break
			}
		}
		if !applied && logger != nil {
			logger.Warn("strategy override key does not match any datapoint",
				"code", dterrors.CodeConfigWarning,
				"device_id", device.DeviceID, "key", override.GetKey())
		}
	}
	return device, nil
}

func connectorConfigFromPayload(payload *dtv1.ConnectorConfigPayload, connectors *connector.Manager) (config.ConnectorConfig, error) {
	if payload == nil {
		return config.ConnectorConfig{}, errors.New("connector_config payload is required")
	}
	if payload.GetConnectorId() == "" || payload.GetProtocol() == "" {
		return config.ConnectorConfig{}, errors.New("connector_config requires connector_id and protocol")
	}
	cfg, _ := connectors.ConnectorConfig(payload.GetConnectorId())
	cfg.ConnectorID = payload.GetConnectorId()
	cfg.Protocol = payload.GetProtocol()
	cfg.DefaultTags = cloneStringMap(payload.GetDefaultTags())
	if strategy := payload.GetReportStrategy(); strategy != nil {
		cfg.ReportStrategy = config.ReportStrategyConfig{
			Mode:          strategy.GetMode().String(),
			PeriodSeconds: int(strategy.GetPeriodSeconds()),
			Deadband:      strategy.GetDeadband(),
		}
	}
	if len(payload.GetConnection()) > 0 {
		if err := json.Unmarshal(payload.GetConnection(), &cfg.Connection); err != nil {
			return config.ConnectorConfig{}, fmt.Errorf("decode connector connection: %w", err)
		}
	}
	if len(payload.GetPolling()) > 0 {
		if err := json.Unmarshal(payload.GetPolling(), &cfg.Polling); err != nil {
			return config.ConnectorConfig{}, fmt.Errorf("decode connector polling: %w", err)
		}
	}
	if len(payload.GetConverter()) > 0 {
		var converter struct {
			ActionMappings map[string]config.ActionMapping `json:"action_mappings"`
		}
		if err := json.Unmarshal(payload.GetConverter(), &converter); err != nil {
			return config.ConnectorConfig{}, fmt.Errorf("decode connector converter: %w", err)
		}
		if converter.ActionMappings != nil {
			cfg.ActionMappings = converter.ActionMappings
		}
	}
	return cfg, nil
}

func success(updateID string, message string) *dtv1.ConfigUpdateResponse {
	return &dtv1.ConfigUpdateResponse{Success: true, ErrorMessage: message, UpdateId: updateID}
}

func failure(updateID string, message string) *dtv1.ConfigUpdateResponse {
	return &dtv1.ConfigUpdateResponse{Success: false, ErrorMessage: message, UpdateId: updateID}
}

func cloneResponse(response *dtv1.ConfigUpdateResponse) *dtv1.ConfigUpdateResponse {
	if response == nil {
		return nil
	}
	return proto.Clone(response).(*dtv1.ConfigUpdateResponse)
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

func cloneBytes(in []byte) []byte {
	if len(in) == 0 {
		return nil
	}
	return append([]byte(nil), in...)
}

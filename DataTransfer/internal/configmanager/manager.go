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
)

type Manager struct {
	connectors *connector.Manager
	logger     *slog.Logger

	mu        sync.Mutex
	updates   map[string]*dtv1.ConfigUpdateResponse
	revisions map[string]int64
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

func (m *Manager) Apply(update *dtv1.DeviceConfigUpdate) *dtv1.ConfigUpdateResponse {
	if update == nil {
		return failure("", "configuration update is nil")
	}
	m.mu.Lock()
	if existing, ok := m.updates[update.GetUpdateId()]; ok {
		m.mu.Unlock()
		return cloneResponse(existing)
	}
	if update.GetUpdateId() == "" {
		m.mu.Unlock()
		return failure("", "update_id is required")
	}
	entityKey, err := entityRevisionKey(update)
	if err != nil {
		response := failure(update.GetUpdateId(), err.Error())
		m.updates[update.GetUpdateId()] = response
		m.mu.Unlock()
		return cloneResponse(response)
	}
	if revision := m.revisions[entityKey]; revision > 0 && update.GetEntityRevision() <= revision {
		response := success(update.GetUpdateId(), "stale configuration update ignored")
		m.updates[update.GetUpdateId()] = response
		m.mu.Unlock()
		m.logger.Warn("stale configuration update ignored", "update_id", update.GetUpdateId(), "entity", entityKey, "revision", update.GetEntityRevision(), "applied_revision", revision)
		return cloneResponse(response)
	}
	m.mu.Unlock()

	applyErr := m.apply(update)

	m.mu.Lock()
	defer m.mu.Unlock()
	var response *dtv1.ConfigUpdateResponse
	if applyErr != nil {
		response = failure(update.GetUpdateId(), applyErr.Error())
	} else {
		response = success(update.GetUpdateId(), "")
		m.revisions[entityKey] = update.GetEntityRevision()
	}
	m.updates[update.GetUpdateId()] = response
	m.logger.Info("configuration update handled", "update_id", update.GetUpdateId(), "entity", entityKey, "revision", update.GetEntityRevision(), "success", response.GetSuccess())
	return cloneResponse(response)
}

func (m *Manager) apply(update *dtv1.DeviceConfigUpdate) error {
	if m.connectors == nil {
		return errors.New("connector manager is not attached")
	}
	switch update.GetAction() {
	case dtv1.DeviceConfigUpdate_ADD_DEVICE, dtv1.DeviceConfigUpdate_UPDATE_DEVICE:
		payload := update.GetDeviceConfig()
		device, err := deviceConfigFromPayload(payload)
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
		return nil
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

func deviceConfigFromPayload(payload *dtv1.DeviceConfigPayload) (config.DeviceConfig, error) {
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
	copy := *response
	return &copy
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

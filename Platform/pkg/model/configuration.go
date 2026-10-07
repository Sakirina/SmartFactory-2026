package model

import (
	"encoding/json"
	"strconv"
)

func ConnectorSecretReference(id string, version int64) string {
	return id + ":" + strconv.FormatInt(version, 10)
}

type ConnectorConfiguration struct {
	ID            string          `json:"id"`
	ConnectorID   string          `json:"connector_id"`
	EdgeID        string          `json:"edge_id"`
	GroupID       string          `json:"group_id"`
	Protocol      string          `json:"protocol"`
	Version       int64           `json:"version"`
	Config        json.RawMessage `json:"config"`
	CredentialRef string          `json:"credential_ref,omitempty"`
	UpdatedMS     int64           `json:"updated_ms"`
	Actor         Actor           `json:"actor"`
}

type ConnectorConfigurationReceipt struct {
	ID                   string `json:"id"`
	ConfigurationID      string `json:"configuration_id"`
	ConfigurationVersion int64  `json:"configuration_version"`
	SourceID             string `json:"source_id"`
	Status               string `json:"status"`
	Reason               string `json:"reason,omitempty"`
	AtMS                 int64  `json:"at_ms"`
	Version              int64  `json:"version"`
}

type ConnectorConfigurationDetail struct {
	Configuration  ConnectorConfiguration        `json:"configuration"`
	Receipt        ConnectorConfigurationReceipt `json:"receipt"`
	AllowedActions []BusinessAction              `json:"allowed_actions"`
}

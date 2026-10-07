package configmanager

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func (m *Manager) ConnectorConfiguration(id string, expected ...*dt.ConnectorConfigPayload) *dt.ConnectorConfigurationState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := &dt.ConnectorConfigurationState{ConnectorId: id}
	out.AppliedEntityRevision = m.revisions["connector:"+id]
	out.AppliedUpdateId = m.appliedUpdates["connector:"+id]
	if m.connectors == nil {
		return out
	}
	c, ok := m.connectors.ConnectorConfig(id)
	if !ok {
		return out
	}
	if len(expected) > 0 && expected[0] != nil {
		want, err := connectorConfigFromPayload(expected[0], m.connectors)
		if err == nil {
			c.Devices = nil
			want.Devices = nil
			actual, _ := json.Marshal(c)
			desired, _ := json.Marshal(want)
			out.MatchesExpected = string(actual) == string(desired)
		}
	}
	c.Connection.Password = ""
	// Devices belong to their independent revision domain.
	c.Devices = nil
	raw, err := json.Marshal(c)
	if err != nil {
		return out
	}
	digest := sha256.Sum256(raw)
	out.Found = true
	out.AppliedEntityRevision = m.revisions["connector:"+id]
	out.AppliedUpdateId = m.appliedUpdates["connector:"+id]
	out.PublicConfigurationSha256 = hex.EncodeToString(digest[:])
	return out
}

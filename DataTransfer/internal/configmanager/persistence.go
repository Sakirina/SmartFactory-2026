package configmanager

import (
	"context"
	"encoding/json"
	"fmt"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/state"
	"google.golang.org/protobuf/proto"
)

type persistedSnapshot struct {
	Version    int                       `json:"version"`
	Connectors []config.ConnectorConfig  `json:"connectors"`
	Revisions  map[string]int64          `json:"revisions"`
	Global     *dtv1.GlobalConfigPayload `json:"global,omitempty"`
}

func (m *Manager) snapshot() persistedSnapshot {
	snapshot := persistedSnapshot{Version: 1, Connectors: m.connectors.ConnectorConfigs(), Revisions: make(map[string]int64, len(m.revisions))}
	for key, value := range m.revisions {
		snapshot.Revisions[key] = value
	}
	if m.globalConfig != nil {
		snapshot.Global = proto.Clone(m.globalConfig).(*dtv1.GlobalConfigPayload)
	}
	if provider, ok := m.global.(interface {
		GlobalConfigSnapshot() *dtv1.GlobalConfigPayload
	}); ok {
		snapshot.Global = provider.GlobalConfigSnapshot()
	}
	return snapshot
}

// AttachJournal must run before starting network listeners and collection workers.
func (m *Manager) AttachJournal(ctx context.Context, journal *state.Store) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := journal.LoadSnapshot(ctx)
	if err != nil {
		return err
	}
	if len(data) > 0 {
		var snapshot persistedSnapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return fmt.Errorf("decode configuration snapshot: %w", err)
		}
		if snapshot.Version != 1 {
			return fmt.Errorf("unsupported configuration snapshot version %d", snapshot.Version)
		}
		if err := m.restore(snapshot); err != nil {
			return fmt.Errorf("restore configuration snapshot: %w", err)
		}
	}
	m.journal = journal
	return nil
}

func (m *Manager) restore(snapshot persistedSnapshot) error {
	wanted := make(map[string]bool, len(snapshot.Connectors))
	for _, cfg := range snapshot.Connectors {
		if err := m.connectors.ApplyConnector(cfg); err != nil {
			return err
		}
		wanted[cfg.ConnectorID] = true
	}
	for _, cfg := range m.connectors.ConnectorConfigs() {
		if !wanted[cfg.ConnectorID] {
			if err := m.connectors.RemoveConnector(cfg.ConnectorID); err != nil {
				return err
			}
		}
	}
	if snapshot.Global != nil && m.global != nil {
		if err := m.global.ApplyGlobalConfig(snapshot.Global); err != nil {
			return err
		}
	}
	m.globalConfig = snapshot.Global
	m.revisions = snapshot.Revisions
	if m.revisions == nil {
		m.revisions = map[string]int64{}
	}
	return nil
}

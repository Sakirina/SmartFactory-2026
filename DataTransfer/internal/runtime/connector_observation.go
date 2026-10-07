package runtime

import dt "competition2026/product/datatransfer/gen/datatransfer/v1"

func (r *Runtime) ConnectorConfiguration(id string, expected ...*dt.ConnectorConfigPayload) *dt.ConnectorConfigurationState {
	r.mu.RLock()
	manager := r.configs
	r.mu.RUnlock()
	if manager == nil {
		return &dt.ConnectorConfigurationState{ConnectorId: id}
	}
	return manager.ConnectorConfiguration(id, expected...)
}

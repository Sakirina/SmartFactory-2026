package configcenter

import (
	"context"
	"errors"
	"fmt"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func modelSchema() map[string]any {
	return map[string]any{
		"type": "object", "required": []string{"endpoint", "model", "api_key", "timeout_ms"}, "additionalProperties": false,
		"properties": map[string]any{
			"endpoint": map[string]any{"type": "string", "maxLength": 2048}, "model": map[string]any{"type": "string", "maxLength": 200},
			"api_key": map[string]any{"type": "string", "maxLength": 16384}, "timeout_ms": map[string]any{"type": "integer", "minimum": 1000, "maximum": 300000},
			"provider": map[string]any{"type": "string", "enum": []string{"openai", "openai-compatible"}},
			"api":      map[string]any{"type": "string", "enum": []string{"chat_completions", "responses"}}, "stream": map[string]any{"type": "boolean"},
		},
	}
}

// UpgradeModelSchema creates a configuration revision at the authoritative
// config node. Existing ciphertext and acknowledgements remain attached to
// their original revisions; a new receipt has a distinct id:version key.
func (s *Service) UpgradeModelSchema(ctx context.Context) error {
	legacy := modelSchema()
	properties := legacy["properties"].(map[string]any)
	for _, name := range []string{"provider", "api", "stream"} {
		delete(properties, name)
	}
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		doc, err := tx.Get("parameter", "ai.model")
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		parameter, err := store.Decode[Parameter](doc)
		if err != nil {
			return err
		}
		// Custom schemas keep their constraints. Only the shipped legacy schema
		// has a known, backward-compatible metadata upgrade.
		if store.Hash(parameter.Schema) != store.Hash(legacy) {
			return nil
		}
		parameter.Schema = modelSchema()
		parameter.Version++
		parameter.State = "pending"
		if !parameter.Dynamic {
			parameter.State = "restart_required"
		}
		if _, err = tx.Put("parameter", parameter.ID, doc.Version, parameter); err != nil {
			return err
		}
		public := parameter
		if public.Secret {
			public.Value = "********"
		}
		if err = tx.Enqueue(fmt.Sprintf("config:%s:%d", parameter.ID, parameter.Version), "config_update", parameter.Program, public); err != nil {
			return err
		}
		return tx.Audit(model.Actor{UserID: "bootstrap", Source: "schema_upgrade"}, "config.schema_upgrade", parameter.ID, "", public)
	})
}

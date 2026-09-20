package cloudsync

import (
	"context"
	"errors"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type NodeProgress struct {
	Downloaded int64 `json:"downloaded"`
	Offered    int64 `json:"offered"`
	UpdatedMS  int64 `json:"updated_ms"`
}

func (s *Server) acknowledgeCursor(ctx context.Context, node string, cursor int64) error {
	var maximum int64
	if err := s.Store.DB.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM sync_changes").Scan(&maximum); err != nil {
		return err
	}
	if cursor < 0 || cursor > maximum {
		return errors.New("invalid node download cursor")
	}
	var progress NodeProgress
	exists := false
	if doc, err := s.Store.Get(ctx, "sync_node_progress", node); err == nil {
		exists = true
		progress, err = store.Decode[NodeProgress](doc)
		if err != nil {
			return err
		}
		if cursor > progress.Offered {
			return errors.New("node acknowledged changes that were not offered")
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if exists && progress.Downloaded == cursor {
		return nil
	}
	progress.Downloaded = cursor
	progress.UpdatedMS = s.Store.Now().UnixMilli()
	if progress.Offered < cursor {
		progress.Offered = cursor
	}
	return s.Store.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral("sync_node_progress", node, progress) })
}
func (s *Server) offeredCursor(ctx context.Context, node string, cursor int64) error {
	doc, err := s.Store.Get(ctx, "sync_node_progress", node)
	if err != nil {
		return err
	}
	progress, err := store.Decode[NodeProgress](doc)
	if err != nil {
		return err
	}
	if cursor <= progress.Offered {
		return nil
	}
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		doc, err := tx.Get("sync_node_progress", node)
		if err != nil {
			return err
		}
		progress, err := store.Decode[NodeProgress](doc)
		if err != nil {
			return err
		}
		if cursor > progress.Offered {
			progress.Offered = cursor
			return tx.SetEphemeral("sync_node_progress", node, progress)
		}
		return nil
	})
}

// CompleteMetadata closes publication records after every admitted active node
// reports the committed download cursor. New nodes still receive the change log.
func (s *Server) CompleteMetadata(ctx context.Context) error {
	docs, err := s.Store.List(ctx, "entity")
	if err != nil {
		return err
	}
	var lowest int64
	registered := false
	for _, doc := range docs {
		entity, e := store.Decode[model.Entity](doc)
		if e != nil {
			return e
		}
		if entity.Kind != "edge" || entity.Status != "active" {
			continue
		}
		var registration Registration
		if len(entity.Config) == 0 {
			continue
		}
		if e := store.DecodeJSON(entity.Config, &registration); e != nil {
			return e
		}
		if registration.CertificateSHA256 == "" {
			continue
		}
		progress := NodeProgress{}
		if saved, e := s.Store.Get(ctx, "sync_node_progress", entity.ID); e == nil {
			progress, e = store.Decode[NodeProgress](saved)
			if e != nil {
				return e
			}
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if !registered || progress.Downloaded < lowest {
			lowest = progress.Downloaded
		}
		registered = true
	}
	for _, kind := range []string{"edge_definition", "entity_sync"} {
		items, e := s.Store.Deliveries(ctx, kind, 1000)
		if e != nil {
			return e
		}
		for _, item := range items {
			var value struct {
				ID      string `json:"id"`
				Version int64  `json:"version"`
			}
			if e = store.DecodeJSON(item.Payload, &value); e != nil {
				return e
			}
			documentKind := "entity"
			if kind == "edge_definition" {
				documentKind = "definition"
			}
			var sequence int64
			if e = s.Store.DB.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM sync_changes WHERE kind=$1 AND id=$2 AND version=$3", documentKind, value.ID, value.Version).Scan(&sequence); e != nil {
				return e
			}
			if sequence > 0 && (!registered || lowest >= sequence) {
				if e = s.Store.DeliveryDone(ctx, item.ID); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

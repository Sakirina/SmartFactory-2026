package coordination

import (
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"context"
	"fmt"
	"strconv"
)

func (c *Coordinator) PublishState(ctx context.Context) error {
	state := NodeState{NodeID: c.Store.NodeID, AtMS: c.Store.Now().UnixMilli(), Definitions: map[string]string{}, Observations: []store.IngestBatch{}}
	docs, e := c.Store.List(ctx, "definition")
	if e != nil {
		return e
	}
	wanted := map[string]map[string]bool{}
	add := func(device, key string) {
		if wanted[device] == nil {
			wanted[device] = map[string]bool{}
		}
		wanted[device][key] = true
	}
	for _, doc := range docs {
		versions, e := c.Store.Versions(ctx, "definition", doc.ID)
		if e != nil {
			return e
		}
		for _, version := range versions {
			definition, e := store.Decode[model.Definition](version)
			if e != nil {
				return e
			}
			if definition.Status != "published" {
				continue
			}
			state.Definitions[definition.ID+":"+strconv.FormatInt(definition.Version, 10)] = store.Hash(definition)
			if len(definition.Policy.EdgeIDs) < 2 {
				continue
			}
			for _, condition := range definition.Policy.Conditions {
				add(condition.DeviceID, condition.Key)
			}
			for _, device := range definition.Selector.DeviceIDs {
				for _, key := range definition.Selector.Keys {
					add(device, key)
				}
			}
		}
	}
	for device, keys := range wanted {
		doc, e := c.Store.Get(ctx, "entity", device)
		if e != nil {
			continue
		}
		entity, e := store.Decode[model.Entity](doc)
		if e != nil {
			return e
		}
		if entity.EdgeID != c.Store.NodeID || entity.Status != "approved" {
			continue
		}
		for key := range keys {
			point, e := c.Store.Latest(ctx, device, key)
			if e != nil {
				continue
			}
			if point.OriginID == "" {
				point.OriginID = point.ID
			}
			state.Observations = append(state.Observations, store.IngestBatch{MessageID: "site:" + point.ID, SourceID: c.Store.NodeID, Points: []model.Observation{point}})
		}
	}
	raw, e := c.pack(state)
	if e != nil {
		return e
	}
	if len(raw) > 2<<20 {
		return fmt.Errorf("required site state exceeds 2 MiB")
	}
	_, e = c.State.Put(key(c.Store.NodeID), raw)
	return e
}
func (c *Coordinator) ReceiveStates(ctx context.Context) error {
	entities, e := c.Store.List(ctx, "entity")
	if e != nil {
		return e
	}
	for _, doc := range entities {
		entity, e := store.Decode[model.Entity](doc)
		if e != nil {
			return e
		}
		if entity.Kind != "edge" || entity.ID == c.Store.NodeID || entity.Status != "active" {
			continue
		}
		current, e := c.get(ctx, c.State, key(entity.ID))
		if e != nil {
			continue
		}
		var state NodeState
		signer, e := c.unpack(ctx, current.Value, &state)
		if e != nil {
			return e
		}
		if signer != entity.ID || state.NodeID != entity.ID {
			return fmt.Errorf("site state signer mismatch")
		}
		age := c.Store.Now().UnixMilli() - state.AtMS
		if age < -5000 || age > c.Store.Policy().OfflineMS {
			continue
		}
		for _, batch := range state.Observations {
			if batch.SourceID != entity.ID || len(batch.Points) != 1 {
				return fmt.Errorf("invalid shared observation")
			}
			point := batch.Points[0]
			ownerDoc, e := c.Store.Get(ctx, "entity", point.DeviceID)
			if e != nil {
				return e
			}
			owner, e := store.Decode[model.Entity](ownerDoc)
			if e != nil {
				return e
			}
			if owner.EdgeID != signer || owner.Status != "approved" {
				return fmt.Errorf("shared observation owner mismatch")
			}
			if point.OriginID == "" || batch.MessageID != "site:"+point.ID {
				return fmt.Errorf("shared observation lost stable identity")
			}
			if _, e = c.Store.Ingest(ctx, batch); e != nil {
				return e
			}
		}
		if e = c.Store.Write(ctx, func(t *store.Tx) error {
			return t.SetEphemeral("source", entity.ID, model.SourceState{ID: entity.ID, LastSeenMS: state.AtMS, Status: "online", Backfill: "complete"})
		}); e != nil {
			return e
		}
	}
	return nil
}

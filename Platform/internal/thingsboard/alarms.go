package thingsboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (a *Adapter) Alarm(ctx context.Context, alarm model.Alarm) error {
	doc, err := a.Store.Get(ctx, "entity", alarm.EntityID)
	if err != nil {
		return err
	}
	entity, err := store.Decode[model.Entity](doc)
	if err != nil {
		return err
	}
	mapped, err := a.EnsureEntity(ctx, entity)
	if err != nil {
		return err
	}
	var native map[string]any
	if saved, e := a.Store.Get(ctx, "tb_alarm_mapping", alarm.ID); e == nil {
		var item struct {
			Native  map[string]any `json:"native"`
			Version int64          `json:"version"`
		}
		if err = store.DecodeJSON(saved.Data, &item); err != nil {
			return err
		}
		if item.Version >= alarm.Version {
			return nil
		}
		native = item.Native
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	typ := "sf:" + alarm.DefinitionID + ":" + alarm.ID
	if native == nil {
		var page struct {
			Data []map[string]any `json:"data"`
		}
		if err = a.Client.Do(ctx, "GET", fmt.Sprintf("/api/alarm/%s/%s?pageSize=100&page=0&textSearch=%s", mapped.Native.EntityType, mapped.Native.ID, url.QueryEscape(typ)), nil, &page); err != nil {
			return err
		}
		for _, item := range page.Data {
			if item["type"] == typ {
				native = item
				break
			}
		}
	}
	if native == nil {
		native = map[string]any{"originator": mapped.Native, "type": typ}
	}
	native["severity"] = alarm.Severity
	native["startTs"] = alarm.StartedMS
	native["endTs"] = alarm.UpdatedMS
	native["details"] = map[string]any{"sf_alarm_id": alarm.ID, "sf_definition_id": alarm.DefinitionID, "sf_version": alarm.Version, "sf_count": alarm.Count, "sf_historical": alarm.Historical, "sf_revision_status": alarm.RevisionStatus, "sf_value": alarm.Value}
	native["acknowledged"] = alarm.Acknowledged
	native["cleared"] = !alarm.Active
	native["clearTs"] = alarm.ClearedMS
	if err = a.Client.Do(ctx, "POST", "/api/alarm", native, &native); err != nil {
		return err
	}
	raw, _ := json.Marshal(native["id"])
	var id EntityID
	_ = json.Unmarshal(raw, &id)
	if id.ID == "" {
		return errors.New("native alarm id missing")
	}
	_, err = a.Store.Put(ctx, "tb_alarm_mapping", alarm.ID, -1, map[string]any{"native": native, "version": alarm.Version})
	return err
}

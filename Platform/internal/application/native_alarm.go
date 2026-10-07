package application

import (
	"context"
	"errors"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// UpdateNativeAlarm is called only by trusted adapters after transport identity
// validation. It additionally verifies the exact native mapping in the same commit.
func (s *Business) UpdateNativeAlarm(ctx context.Context, id string, input model.NativeAlarmUpdate) (model.NativeAlarmState, error) {
	var result model.NativeAlarmState
	if input.SourceID != "thingsboard:"+s.NodeID || input.NativeID == "" || input.StartedMS <= 0 || input.StartedMS > 1<<60 || input.AcknowledgedMS < 0 || input.AcknowledgedMS > 1<<60 || input.ClearedMS < 0 || input.ClearedMS > 1<<60 || input.SourceVersion != model.NativeAlarmVersion(input) {
		return result, identity.ErrDenied
	}
	if (!input.Acknowledged && input.AcknowledgedMS != 0) || (!input.Cleared && input.ClearedMS != 0) {
		return result, errors.New("native lifecycle timestamps disagree with status")
	}
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		mapping, e := tx.Get("tb_alarm_mapping", id)
		if e != nil {
			return e
		}
		var bound struct {
			Native struct {
				ID struct {
					ID string `json:"id"`
				} `json:"id"`
			} `json:"native"`
		}
		if e = store.DecodeJSON(mapping.Data, &bound); e != nil {
			return e
		}
		if bound.Native.ID.ID != input.NativeID {
			return identity.ErrDenied
		}
		alarmDoc, e := tx.Get("alarm", id)
		if e != nil {
			return e
		}
		alarm, e := store.Decode[model.Alarm](alarmDoc)
		if e != nil {
			return e
		}
		if alarm.StartedMS != input.StartedMS {
			return store.ErrConflict
		}
		previous, e := tx.Get("native_alarm_state", id)
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if e == nil {
			result, e = store.Decode[model.NativeAlarmState](previous)
			if e != nil {
				return e
			}
			if input.SourceVersion < result.SourceVersion {
				return nil
			}
			if input.SourceVersion == result.SourceVersion {
				if store.Hash(input) != store.Hash(result.NativeAlarmUpdate) {
					return store.ErrConflict
				}
				return nil
			}
			if result.Acknowledged && !input.Acknowledged || result.Cleared && !input.Cleared {
				return store.ErrConflict
			}
		}
		result = model.NativeAlarmState{ID: id, NativeAlarmUpdate: input, ObservedMS: s.Store.CurrentTime().UnixMilli(), Version: previous.Version + 1}
		if _, e = tx.Put("native_alarm_state", id, previous.Version, result); e != nil {
			return e
		}
		if input.Acknowledged {
			if e = tx.AcknowledgeAlarm(id); e != nil {
				return e
			}
		}
		return tx.Audit(model.Actor{UserID: input.SourceID, Source: "native-alarm"}, "alarm.native.lifecycle", alarm.EntityID, id, result)
	})
	return result, err
}

func (s *Business) nativeAlarmDetail(ctx context.Context, id string, detail *model.AlarmDetail) error {
	detail.NativeSync = model.NativeAlarmSyncStatus{SourceID: "thingsboard:" + s.NodeID, IntervalMS: 5000, BatchLimit: 64, TimeoutMS: 10000, Status: "disabled"}
	if doc, e := s.Store.Get(ctx, "native_alarm_sync", s.NodeID); e == nil {
		if e = store.DecodeJSON(doc.Data, &detail.NativeSync); e != nil {
			return e
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	detail.NativeSync.Cursor = ""
	if doc, e := s.Store.Get(ctx, "native_alarm_state", id); e == nil {
		value, e := store.Decode[model.NativeAlarmState](doc)
		if e != nil {
			return e
		}
		detail.Native = &value
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	return nil
}

package thingsboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func nativeNumber(value any) (int64, error) {
	switch v := value.(type) {
	case nil:
		return 0, nil
	case json.Number:
		return v.Int64()
	case int64:
		return v, nil
	case float64:
		if v == float64(int64(v)) {
			return int64(v), nil
		}
	case int:
		return int64(v), nil
	}
	return 0, errors.New("native alarm timestamp is not an integer")
}
func NativeAlarmUpdate(source string, native map[string]any) (model.NativeAlarmUpdate, error) {
	result := model.NativeAlarmUpdate{SourceID: source}
	raw, _ := json.Marshal(native["id"])
	var id EntityID
	if e := json.Unmarshal(raw, &id); e != nil {
		return result, e
	}
	result.NativeID = id.ID
	var e error
	result.StartedMS, e = nativeNumber(native["startTs"])
	if e != nil {
		return result, e
	}
	result.AcknowledgedMS, e = nativeNumber(native["ackTs"])
	if e != nil {
		return result, e
	}
	result.ClearedMS, e = nativeNumber(native["clearTs"])
	if e != nil {
		return result, e
	}
	result.Acknowledged, _ = native["acknowledged"].(bool)
	result.Cleared, _ = native["cleared"].(bool)
	result.SourceVersion = model.NativeAlarmVersion(result)
	if result.NativeID == "" {
		return result, errors.New("native alarm id is missing")
	}
	return result, nil
}

// PollAlarmStates reads at most 64 bound alarms per round. A persisted cursor
// makes all mappings eligible without holding database connections during HTTP.
func (a *Adapter) PollAlarmStates(ctx context.Context, apply func(context.Context, string, model.NativeAlarmUpdate) (model.NativeAlarmState, error)) (resultErr error) {
	a.alarmPollMu.Lock()
	defer a.alarmPollMu.Unlock()
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	status := model.NativeAlarmSyncStatus{SourceID: "thingsboard:" + a.Store.NodeID, Enabled: true, IntervalMS: 5000, BatchLimit: 64, TimeoutMS: 10000, Status: "pending"}
	if doc, e := a.Store.Get(ctx, "native_alarm_sync", a.Store.NodeID); e == nil {
		if e = store.DecodeJSON(doc.Data, &status); e != nil {
			return e
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	status.AttemptMS = a.Store.Now().UnixMilli()
	status.Enabled = true
	status.BatchLimit = 64
	status.TimeoutMS = 10000
	defer func() {
		if parent.Err() != nil {
			return
		}
		persist, end := context.WithTimeout(parent, 2*time.Second)
		defer end()
		if e := a.Store.DB.QueryRowContext(persist, "SELECT count(*) FROM documents WHERE kind='native_alarm_sync_failure'").Scan(&status.FailedMappings); e != nil {
			resultErr = errors.Join(resultErr, e)
		}
		if resultErr != nil || status.FailedMappings > 0 {
			status.Failures++
			status.Status = "retrying"
			status.Reason = "native alarm lifecycle could not be synchronized"
		} else {
			status.Failures = 0
			status.SuccessMS = a.Store.Now().UnixMilli()
			status.Status = "current"
			status.Reason = ""
		}
		e := a.Store.Write(persist, func(tx *store.Tx) error { return tx.SetEphemeral("native_alarm_sync", a.Store.NodeID, status) })
		resultErr = errors.Join(resultErr, e)
	}()
	rows, e := a.Store.DB.QueryContext(ctx, "SELECT id,data FROM documents WHERE kind='tb_alarm_mapping' AND id>$1 ORDER BY id LIMIT 64", status.Cursor)
	if e != nil {
		return e
	}
	type item struct {
		id   string
		data []byte
	}
	items := []item{}
	for rows.Next() {
		var v item
		var raw string
		if e = rows.Scan(&v.id, &raw); e != nil {
			rows.Close()
			return e
		}
		v.data = []byte(raw)
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	processed := 0
	for _, v := range items {
		if e := ctx.Err(); e != nil {
			resultErr = errors.Join(resultErr, e)
			break
		}
		stage := "mapping"
		attempt := func() error {
			var mapping struct {
				Native map[string]any `json:"native"`
			}
			if e = store.DecodeJSON(v.data, &mapping); e != nil {
				return e
			}
			state, e := NativeAlarmUpdate(status.SourceID, mapping.Native)
			if e != nil {
				return e
			}
			var native map[string]any
			stage = "native_read"
			if e = a.Client.Do(ctx, "GET", "/api/alarm/"+url.PathEscape(state.NativeID), nil, &native); e != nil {
				return e
			}
			state, e = NativeAlarmUpdate(status.SourceID, native)
			if e != nil {
				return e
			}
			if _, e = apply(ctx, v.id, state); e != nil {
				stage = "application"
				return e
			}
			return nil
		}
		failure := attempt()
		// Advance over every attempted mapping. Failed mappings retain a local
		// retry record and become eligible again when the cursor wraps.
		status.Cursor = v.id
		processed++
		if parent.Err() != nil {
			return parent.Err()
		}
		persist, end := context.WithTimeout(parent, 2*time.Second)
		e := a.Store.Write(persist, func(tx *store.Tx) error {
			if failure == nil {
				return tx.Delete("native_alarm_sync_failure", v.id)
			}
			httpStatus := 0
			var h HTTPError
			if errors.As(failure, &h) {
				httpStatus = h.Status
			}
			return tx.SetEphemeral("native_alarm_sync_failure", v.id, map[string]any{"alarm_id": v.id, "stage": stage, "http_status": httpStatus, "attempt_ms": a.Store.Now().UnixMilli()})
		})
		end()
		if failure != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("native alarm polling %s failed", stage))
		}
		if e != nil {
			resultErr = errors.Join(resultErr, e)
			break
		}
	}
	if len(items) < 64 && processed == len(items) {
		status.Cursor = ""
	}
	return resultErr
}

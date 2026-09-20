package store

import (
	"encoding/json"
	"fmt"
	"strings"

	"competition2026/product/platform/pkg/model"
)

type seriesKey struct{ device, key string }

// insertRows limits parameter counts for both supported database drivers.
// Table, columns and conflict are internal SQL constants.
func (t *Tx) insertRows(table, columns, conflict string, rows [][]any) error {
	for start := 0; start < len(rows); start += 250 {
		end := min(start+250, len(rows))
		var query strings.Builder
		query.WriteString("INSERT INTO " + table + "(" + columns + ") VALUES ")
		args := make([]any, 0, (end-start)*len(rows[start]))
		for i, row := range rows[start:end] {
			if i > 0 {
				query.WriteByte(',')
			}
			query.WriteByte('(')
			for j, value := range row {
				if j > 0 {
					query.WriteByte(',')
				}
				args = append(args, value)
				fmt.Fprintf(&query, "$%d", len(args))
			}
			query.WriteByte(')')
		}
		query.WriteString(conflict)
		if _, err := t.ExecContext(t.Ctx, query.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) latestTimes(points []model.Observation) (map[seriesKey]int64, error) {
	result := map[seriesKey]int64{}
	if len(points) == 0 {
		return result, nil
	}
	deviceSet, keySet := map[string]bool{}, map[string]bool{}
	devices, keys := []string{}, []string{}
	for _, p := range points {
		if !deviceSet[p.DeviceID] {
			deviceSet[p.DeviceID] = true
			devices = append(devices, p.DeviceID)
		}
		if !keySet[p.Key] {
			keySet[p.Key] = true
			keys = append(keys, p.Key)
		}
	}
	var query strings.Builder
	query.WriteString("SELECT device_id,key,observed_ms FROM latest WHERE 1=1")
	args := []any{}
	appendIn(&query, &args, "device_id", devices)
	appendIn(&query, &args, "key", keys)
	rows, err := t.QueryContext(t.Ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k seriesKey
		var at int64
		if err = rows.Scan(&k.device, &k.key, &at); err != nil {
			return nil, err
		}
		result[k] = at
	}
	return result, rows.Err()
}

func (t *Tx) insertIngestPoints(points []model.Observation) error {
	observations, latestRows, deliveries := [][]any{}, [][]any{}, [][]any{}
	latest := map[seriesKey]model.Observation{}
	encoded := map[string]string{}
	now := t.Store.Now().UnixMilli()
	for _, p := range points {
		if err := t.EnsureDay(p.ObservedMS); err != nil {
			return err
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		data := string(raw)
		observations = append(observations, []any{p.ID, p.ObservedMS, p.MessageID, p.DeviceID, p.Key, p.ReceivedMS, p.Revision, p.Quality, p.DefinitionID, data})
		k := seriesKey{p.DeviceID, p.Key}
		old, exists := latest[k]
		if !exists || p.ObservedMS > old.ObservedMS || (p.ObservedMS == old.ObservedMS && p.ReceivedMS >= old.ReceivedMS) {
			latest[k] = p
			encoded[p.ID] = data
		}
		deliveries = append(deliveries,
			[]any{"engine:" + p.ID, "engine", p.DeviceID, data, now, 0, 0, ""},
			[]any{"tb:" + p.ID, "tb_telemetry", p.DeviceID, data, now, 0, 0, ""})
		if !p.Late {
			deliveries = append(deliveries, []any{"strategy:" + p.ID, "strategy", p.DeviceID, data, now, 0, 0, ""})
		}
	}
	for _, p := range latest {
		latestRows = append(latestRows, []any{p.DeviceID, p.Key, p.ObservedMS, p.ReceivedMS, encoded[p.ID]})
	}
	if err := t.insertRows("observations", "id,observed_ms,message_id,device_id,key,received_ms,revision,quality,definition_id,data", "", observations); err != nil {
		return err
	}
	if err := t.insertRows("latest", "device_id,key,observed_ms,received_ms,data", " ON CONFLICT(device_id,key) DO UPDATE SET observed_ms=excluded.observed_ms,received_ms=excluded.received_ms,data=excluded.data WHERE excluded.observed_ms>latest.observed_ms OR (excluded.observed_ms=latest.observed_ms AND excluded.received_ms>=latest.received_ms)", latestRows); err != nil {
		return err
	}
	return t.insertRows("outbox", "id,kind,destination,payload,created_ms,attempts,next_ms,last_error", " ON CONFLICT(id) DO NOTHING", deliveries)
}

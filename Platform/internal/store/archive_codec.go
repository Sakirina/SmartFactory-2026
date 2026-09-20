package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"competition2026/product/platform/pkg/model"
)

const archiveFormat = "smartfactory-observations-columns-v2"

type archiveReference struct {
	Field  string `json:"field"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}
type archiveColumnsPayload struct {
	Format     string                      `json:"format"`
	BaseMS     int64                       `json:"base_ms"`
	Constants  map[string]json.RawMessage  `json:"constants"`
	References map[string]archiveReference `json:"references,omitempty"`
	Fields     []string                    `json:"fields"`
	Rows       [][]json.RawMessage         `json:"rows"`
}

// packArchive factors repeated fields and reversible identifier relationships.
// JSON numbers remain uninterpreted bytes until the normal observation decoder.
func packArchive(points []model.Observation) ([]byte, error) {
	payload := archiveColumnsPayload{Format: archiveFormat, BaseMS: points[0].ObservedMS, Constants: map[string]json.RawMessage{}, References: map[string]archiveReference{}}
	records := make([]map[string]json.RawMessage, len(points))
	allFields := map[string]bool{}
	size := 0
	for i, p := range points {
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		size += len(raw)
		if size > archivePlainLimit {
			return nil, errors.New("archive original observations exceed byte budget")
		}
		if err = json.Unmarshal(raw, &records[i]); err != nil {
			return nil, err
		}
		if p.ObservedMS < payload.BaseMS {
			payload.BaseMS = p.ObservedMS
		}
	}
	// Choose the shortest available identifier as the common text source.
	idFields := []string{"id", "message_id", "origin_id"}
	bestBase := ""
	bestCost := int(^uint(0) >> 1)
	for _, name := range idFields {
		cost := 0
		valid := true
		for _, record := range records {
			var value string
			if err := json.Unmarshal(record[name], &value); err != nil || value == "" {
				valid = false
				break
			}
			cost += len(value)
		}
		if valid && cost < bestCost {
			bestBase = name
			bestCost = cost
		}
	}
	if bestBase != "" {
		for _, target := range idFields {
			if target == bestBase {
				continue
			}
			var base, value string
			if json.Unmarshal(records[0][bestBase], &base) != nil || json.Unmarshal(records[0][target], &value) != nil {
				continue
			}
			index := strings.Index(value, base)
			if index < 0 {
				continue
			}
			ref := archiveReference{Field: bestBase, Prefix: value[:index], Suffix: value[index+len(base):]}
			valid := true
			for _, record := range records {
				if json.Unmarshal(record[bestBase], &base) != nil || json.Unmarshal(record[target], &value) != nil || value != ref.Prefix+base+ref.Suffix {
					valid = false
					break
				}
			}
			if valid {
				payload.References[target] = ref
				for _, record := range records {
					delete(record, target)
				}
			}
		}
	}
	for i, record := range records {
		observed := points[i].ObservedMS - payload.BaseMS
		received := points[i].ReceivedMS - points[i].ObservedMS
		record["observed_ms"], _ = json.Marshal(observed)
		record["received_ms"], _ = json.Marshal(received)
		for name := range record {
			allFields[name] = true
		}
	}
	for name := range allFields {
		value := records[0][name]
		constant := len(value) > 0
		for _, record := range records {
			if !bytes.Equal(value, record[name]) {
				constant = false
				break
			}
		}
		if constant {
			payload.Constants[name] = value
		} else {
			payload.Fields = append(payload.Fields, name)
		}
	}
	sort.Strings(payload.Fields)
	for _, record := range records {
		row := make([]json.RawMessage, len(payload.Fields))
		for i, name := range payload.Fields {
			row[i] = record[name]
		}
		payload.Rows = append(payload.Rows, row)
	}
	return json.Marshal(payload)
}
func addArchiveTime(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, errors.New("archive timestamp overflows")
	}
	return a + b, nil
}
func unpackArchive(raw []byte, count int64) ([]model.Observation, error) {
	var payload archiveColumnsPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.Format != archiveFormat || int64(len(payload.Rows)) != count || len(payload.Fields) > 64 {
		return nil, errors.New("unsupported archive format or dimensions")
	}
	points := make([]model.Observation, 0, count)
	size := 0
	for _, row := range payload.Rows {
		if len(row) != len(payload.Fields) {
			return nil, errors.New("archive row length mismatch")
		}
		record := map[string]json.RawMessage{}
		for name, value := range payload.Constants {
			record[name] = value
		}
		for i, name := range payload.Fields {
			if _, exists := record[name]; exists {
				return nil, errors.New("archive field appears twice")
			}
			record[name] = row[i]
		}
		for target, ref := range payload.References {
			if ref.Field == target || !(target == "id" || target == "message_id" || target == "origin_id") {
				return nil, errors.New("invalid archive identifier reference")
			}
			if _, exists := record[target]; exists {
				return nil, errors.New("archive identifier appears twice")
			}
			var base string
			if err := json.Unmarshal(record[ref.Field], &base); err != nil {
				return nil, err
			}
			record[target], _ = json.Marshal(ref.Prefix + base + ref.Suffix)
		}
		var observedDelta, receivedDelta int64
		if err := json.Unmarshal(record["observed_ms"], &observedDelta); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(record["received_ms"], &receivedDelta); err != nil {
			return nil, err
		}
		observed, err := addArchiveTime(payload.BaseMS, observedDelta)
		if err != nil {
			return nil, err
		}
		received, err := addArchiveTime(observed, receivedDelta)
		if err != nil {
			return nil, err
		}
		record["observed_ms"], _ = json.Marshal(observed)
		record["received_ms"], _ = json.Marshal(received)
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		size += len(encoded)
		if size > archivePlainLimit {
			return nil, errors.New("archive decoded observations exceed byte budget")
		}
		var point model.Observation
		if err = DecodeJSON(encoded, &point); err != nil {
			return nil, fmt.Errorf("archive observation: %w", err)
		}
		points = append(points, point)
	}
	return points, nil
}

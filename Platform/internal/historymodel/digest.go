package historymodel

import (
	"competition2026/product/platform/pkg/model"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func SnapshotDigest(snapshot Snapshot) (string, error) {
	snapshot.ID = ""
	snapshot.SHA256 = ""
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func InputDigest(s Snapshot) string {
	raw, _ := json.Marshal([]any{s.FromMS, s.ToMS, s.Order, s.Clock, s.Points, s.History, s.InitialState, s.InitialStateRunID, s.AssetVersions})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func InputIdentity(p model.Observation) string {
	if p.OriginID != "" {
		return p.OriginID
	}
	if p.DefinitionID != "" {
		raw, _ := json.Marshal([]any{p.DefinitionID, p.DeviceID, p.Key, p.ObservedMS})
		sum := sha256.Sum256(raw)
		return "derived:" + hex.EncodeToString(sum[:])
	}
	return p.ID
}

// Empty supplied arrays are meaningful snapshots when a Go client serializes
// an input; nil keeps database capture semantics.
func (r HistoryRequest) MarshalJSON() ([]byte, error) {
	type wire HistoryRequest
	raw, err := json.Marshal(wire(r))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for key, value := range map[string]struct {
		present bool
		value   any
	}{"points": {r.Points != nil, r.Points}, "history": {r.History != nil, r.History}, "asset_versions": {r.AssetVersions != nil, r.AssetVersions}} {
		if value.present {
			fields[key], err = json.Marshal(value.value)
			if err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(fields)
}

package api

import (
	"context"
	"encoding/json"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestLatestIndicatorsSurviveTrendWindowAndKeepResourceScope(t *testing.T) {
	s, token := scopedServer(t, false)
	now := s.Store.Now().UnixMilli()
	for _, point := range []model.Observation{
		{ID: "counter", DeviceID: "device-a", Key: "total", Value: 123, Quality: "GOOD", ObservedMS: now - 7200000},
		{ID: "temperature", DeviceID: "device-a", Key: "temperature", Value: 25, Quality: "GOOD", ObservedMS: now - 10},
		{ID: "private", DeviceID: "device-b", Key: "total", Value: 999, Quality: "GOOD", ObservedMS: now - 10},
	} {
		if err := s.Store.Write(context.Background(), func(tx *store.Tx) error { return tx.InsertPoint(point) }); err != nil {
			t.Fatal(err)
		}
	}
	w := call(s, token, "GET", "/api/sf/v1/data?include_latest=true&limit=1&window_ms=60000", nil)
	var result model.DataResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if len(result.Points) != 1 || len(result.Latest) != 2 {
		t.Fatal("trend limit incorrectly removed an indicator", result)
	}
	for _, point := range result.Latest {
		if point.DeviceID != "device-a" {
			t.Fatal("latest values exposed another resource", point)
		}
	}
	if w = call(s, token, "GET", "/api/sf/v1/data?include_latest=true&device_ids=device-b", nil); w.Code != 403 {
		t.Fatal("explicit unauthorized latest query", w.Code)
	}
}

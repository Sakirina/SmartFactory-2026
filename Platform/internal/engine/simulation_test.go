package engine

import (
	"context"
	"fmt"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestDraftSimulationIncludesSampleWithoutChangingStoredData(t *testing.T) {
	s := engineFixture(t)
	ctx := context.Background()
	now := s.Store.Now().UnixMilli()
	p := model.Observation{DeviceID: "device", Key: "temperature", ObservedMS: now, Value: 10, Quality: "GOOD"}
	if _, err := s.Store.Ingest(ctx, store.IngestBatch{MessageID: "stored", SourceID: "edge-a", Points: []model.Observation{p}}); err != nil {
		t.Fatal(err)
	}
	p.ID = "sample"
	p.Value = 30
	p.ObservedMS++
	result, err := s.Simulate(ctx, definition(), p)
	if err != nil || fmt.Sprint(result.Values["result"]) != "20" {
		t.Fatal(result, err)
	}
	var count int
	if err = s.Store.DB.QueryRow("SELECT count(*) FROM observations").Scan(&count); err != nil || count != 1 {
		t.Fatal("simulation wrote data", count, err)
	}
	p.DeviceID = "foreign"
	if _, err = s.Simulate(ctx, definition(), p); err == nil {
		t.Fatal("foreign simulation sample accepted")
	}
}

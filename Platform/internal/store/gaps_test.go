package store

import (
	"competition2026/product/platform/pkg/model"
	"context"
	"path/filepath"
	"testing"
)

func TestGapQueriesRespectTimeAndDeviceScopeAndReplay(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "data.db"), "edge", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	batch := IngestBatch{MessageID: "gap-event", SourceID: "edge", Critical: true, Gaps: []model.DataGap{{DeviceID: "meter", Key: "value", FromMS: 10, ToMS: 20, Missing: 8, Reason: "queue full", Scope: "collection_queue"}}}
	for i := 0; i < 2; i++ {
		result, err := s.Ingest(ctx, batch)
		if err != nil || !result.Committed || result.Duplicate != (i == 1) {
			t.Fatalf("ingest: %+v %v", result, err)
		}
	}
	result, err := s.Query(ctx, Query{DeviceIDs: []string{"meter"}, FromMS: 1, ToMS: 30})
	if err != nil || len(result.Gaps) != 1 || result.Quality.Missing == nil || *result.Quality.Missing != 8 || result.Quality.Completeness != "incomplete" {
		t.Fatalf("query: %+v %v", result, err)
	}
	result, err = s.Query(ctx, Query{DeviceIDs: []string{"other"}, FromMS: 1, ToMS: 30})
	if err != nil || len(result.Gaps) != 0 || result.Quality.Missing != nil {
		t.Fatalf("scope leakage: %+v %v", result, err)
	}
	result, err = s.Query(ctx, Query{DeviceIDs: []string{"meter"}, FromMS: 15, ToMS: 30})
	if err != nil || len(result.Gaps) != 1 || result.Quality.Missing != nil {
		t.Fatalf("partial group count should be unknown: %+v %v", result, err)
	}
}

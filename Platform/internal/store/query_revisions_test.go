package store

import (
	"competition2026/product/platform/pkg/model"
	"context"
	"testing"
)

func TestRecentQueryPreservesRawEventsAndLatestDerivedRevision(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := s.Now().UnixMilli()
	points := []model.Observation{
		{ID: "raw-a", DeviceID: "sensor", Key: "value", ObservedMS: at, ReceivedMS: at, Revision: 1, Value: 1, Quality: "GOOD"},
		{ID: "raw-b", DeviceID: "sensor", Key: "value", ObservedMS: at, ReceivedMS: at, Revision: 1, Value: 2, Quality: "GOOD"},
		{ID: "derived-1", DeviceID: "sensor", Key: "average", DefinitionID: "average", ObservedMS: at, ReceivedMS: at, Revision: 1, Value: 2, Quality: "GOOD"},
		{ID: "derived-2", DeviceID: "sensor", Key: "average", DefinitionID: "average", ObservedMS: at, ReceivedMS: at, Revision: 2, Value: 3, Quality: "GOOD"},
		{ID: "derived-3", DeviceID: "sensor", Key: "average", DefinitionID: "average", ObservedMS: at, ReceivedMS: at + 1, Revision: 2, Value: 4, Quality: "GOOD"},
		{ID: "derived-4", DeviceID: "sensor", Key: "average", DefinitionID: "average", ObservedMS: at, ReceivedMS: at + 1, Revision: 2, Value: 5, Quality: "BAD"},
	}
	if e := s.Write(ctx, func(tx *Tx) error {
		for _, p := range points {
			if e := tx.InsertPoint(p); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	result, e := s.Query(ctx, Query{DeviceIDs: []string{"sensor"}, FromMS: at, ToMS: at, Limit: 100})
	if e != nil || len(result.Points) != 3 || result.Quality.Good != 2 || result.Quality.Bad != 1 {
		t.Fatal(result, e)
	}
	for _, p := range result.Points {
		if p.Key == "average" && p.ID != "derived-4" {
			t.Fatal("wrong derived revision", p)
		}
	}
	result, e = s.Query(ctx, Query{DeviceIDs: []string{"sensor"}, FromMS: at, ToMS: at, Limit: 1})
	if e != nil || !result.Truncated || result.Quality.Good+result.Quality.Bad+result.Quality.Uncertain != 1 {
		t.Fatal("quality includes the overflow row", result, e)
	}
	result, e = s.Query(ctx, Query{DeviceIDs: []string{"sensor"}, FromMS: at, ToMS: at, Limit: 100, RawOnly: true})
	if e != nil || len(result.Points) != 2 {
		t.Fatal("raw events coalesced", result, e)
	}
}

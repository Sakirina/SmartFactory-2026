package buffer

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestEvictionAndGapCommitTogetherAndCriticalEventsRemain(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "buffer.db")
	s := openTestStore(t, path)
	telemetry := testMessage("expired")
	telemetry.Timestamp = time.Now().Add(-2 * time.Hour).UnixMilli()
	telemetry.GetTelemetry().Datapoints = []*dt.Datapoint{{Key: "value", Timestamp: telemetry.Timestamp, Value: &dt.DataValue{Kind: &dt.DataValue_IntValue{IntValue: 1}}, Quality: dt.DataQuality_GOOD}}
	critical := &dt.DeviceMessage{MessageId: "critical", Timestamp: telemetry.Timestamp, Direction: dt.Direction_UPSTREAM, Type: dt.MessageType_EVENT, Payload: &dt.DeviceMessage_Event{Event: &dt.EventPayload{EventType: "important"}}}
	if _, err := s.Enqueue(ctx, telemetry); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(ctx, critical); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_gap BEFORE INSERT ON buffer_gaps BEGIN SELECT RAISE(ABORT,'injected write failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(ctx); err == nil {
		t.Fatal("failed gap write allowed deletion")
	}
	if _, err := s.recordByMessageID(ctx, "expired"); err != nil {
		t.Fatal("telemetry erased without gap", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_gap`); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.recordByMessageID(ctx, "critical"); err != nil {
		t.Fatal("unacknowledged critical event expired", err)
	}
	gaps, err := s.PendingGaps(ctx)
	if err != nil || len(gaps) != len(telemetry.GetTelemetry().Datapoints) {
		t.Fatalf("gap records: %d %v", len(gaps), err)
	}
	s.Close()
	s = openTestStore(t, path)
	defer s.Close()
	reopened, err := s.PendingGaps(ctx)
	if err != nil || len(reopened) != len(gaps) {
		t.Fatalf("gaps lost after restart: %d %v", len(reopened), err)
	}
	for _, gap := range reopened {
		if err = s.GapForwarded(ctx, gap.MessageId); err != nil {
			t.Fatal(err)
		}
	}
	gaps, err = s.PendingGaps(ctx)
	if err != nil || len(gaps) != 0 {
		t.Fatal("gap commit did not clear forwarding queue")
	}
}

package state

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"context"
	"path/filepath"
	"testing"
)

func TestGapSummarySurvivesRestartAndFailedOutboxCommit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gaps.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	message := &dt.DeviceMessage{MessageId: "a", Type: dt.MessageType_TELEMETRY, Device: &dt.DeviceIdentity{DeviceId: "meter"}, Timestamp: 10, Payload: &dt.DeviceMessage_Telemetry{Telemetry: &dt.TelemetryPayload{Datapoints: []*dt.Datapoint{{Key: "value", Timestamp: 10}}}}}
	for i := 0; i < 100; i++ {
		message.Timestamp = int64(10 + i)
		message.GetTelemetry().Datapoints[0].Timestamp = message.Timestamp
		if err = s.RecordGap(ctx, message, "collection_queue", "full"); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.db.Exec(`CREATE TRIGGER fail_gap BEFORE INSERT ON durable_outbox BEGIN SELECT RAISE(ABORT,'injected disk failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err = s.FlushGaps(ctx, true); err == nil {
		t.Fatal("write failure succeeded")
	}
	var count int
	if err = s.db.QueryRow(`SELECT SUM(missing) FROM continuous_gaps`).Scan(&count); err != nil || count != 100 {
		t.Fatalf("gap erased on commit failure: %d %v", count, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_gap`); err != nil {
		t.Fatal(err)
	}
	if err = s.FlushGaps(ctx, true); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(ctx, 1000)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %d %v", len(pending), err)
	}
	first := pending[0]
	if first.GetEvent().Data["missing"] != "100" || first.GetEvent().Data["from_ms"] != "10" || first.GetEvent().Data["to_ms"] != "109" {
		t.Fatalf("summary: %v", first)
	}
	if err = s.RecordGap(ctx, message, "collection_queue", "full"); err != nil {
		t.Fatal(err)
	}
	if err = s.FlushGaps(ctx, true); err != nil {
		t.Fatal(err)
	}
	pending, err = s.Pending(ctx, 1000)
	if err != nil || len(pending) != 2 || pending[1].MessageId == first.MessageId {
		t.Fatalf("immutable group reused: %v %v", pending, err)
	}
}

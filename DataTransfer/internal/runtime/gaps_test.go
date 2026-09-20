package runtime

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	"competition2026/product/datatransfer/internal/state"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func gapTelemetry(id string) *dt.DeviceMessage {
	msg := telemetryMessage(id, "A")
	msg.GetTelemetry().Datapoints = []*dt.Datapoint{{Key: "value", Timestamp: msg.Timestamp, Value: &dt.DataValue{Kind: &dt.DataValue_IntValue{IntValue: 1}}, Quality: dt.DataQuality_GOOD}}
	return msg
}

func TestConsumerBackpressureAndCancellation(t *testing.T) {
	cfg := config.Defaults()
	cfg.Runtime.RingSize = 256
	r := New(cfg)
	defer r.Close()
	journal, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	r.AttachCommandJournal(journal)
	ch, cancel := r.SubscribeFor(Filter{}, "edge-a")
	for i := 0; i < 256; i++ {
		if err = r.Publish(gapTelemetry(strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	finished := make(chan error, 1)
	go func() { finished <- r.Publish(gapTelemetry("blocked")) }()
	select {
	case err := <-finished:
		t.Fatalf("full queue did not wait: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	<-ch
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("consumer did not release backpressure")
	}
	if r.continuousGapTotal.Load() != 0 {
		t.Fatal("blocking policy lost messages")
	}
	cancel()
	if err = journal.FlushGaps(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	batch, err := journal.Pending(context.Background(), 1000)
	if err != nil || len(batch) != 1 || batch[0].GetEvent().Data["missing"] != "256" {
		t.Fatalf("canceled queue missing record: %v %v", batch, err)
	}
}

func TestDropOldestRecordsExactLoss(t *testing.T) {
	cfg := config.Defaults()
	cfg.Runtime.RingSize = 256
	r := New(cfg)
	defer r.Close()
	journal, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	r.AttachCommandJournal(journal)
	manager, err := connector.NewManager(nil, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	r.AttachConnectorManager(manager)
	manager.SetBackpressurePolicy(dt.BackpressurePolicy_BP_DROP_OLDEST)
	ch, cancel := r.SubscribeFor(Filter{}, "edge-a")
	defer cancel()
	for i := 0; i < 300; i++ {
		if err = r.Publish(gapTelemetry(strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	if got := (<-ch).MessageId; got != "44" {
		t.Fatalf("oldest retained %s", got)
	}
	if err = journal.FlushGaps(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	batch, err := journal.Pending(context.Background(), 1000)
	if err != nil || len(batch) != 1 || batch[0].GetEvent().Data["missing"] != "44" {
		t.Fatalf("loss count: %v %v", batch, err)
	}
}

func TestShutdownAndRestartPreserveRegisteredConsumerGaps(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	journal, err := state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	r := New(config.Defaults())
	if err = r.AttachCommandJournal(journal); err != nil {
		t.Fatal(err)
	}
	_, cancel := r.SubscribeFor(Filter{}, "edge-a")
	for i := 0; i < 3; i++ {
		if err = r.Publish(gapTelemetry("shutdown-" + strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	journal.Close()
	journal, err = state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	restarted := New(config.Defaults())
	defer restarted.Close()
	if err = restarted.AttachCommandJournal(journal); err != nil {
		t.Fatal(err)
	}
	// No business stream is open after the process restarts.
	if err = restarted.Publish(gapTelemetry("before-reconnect")); err != nil {
		t.Fatal(err)
	}
	if err = journal.FlushGaps(ctx, true); err != nil {
		t.Fatal(err)
	}
	pending, err := journal.Pending(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	missing := 0
	for _, message := range pending {
		n, _ := strconv.Atoi(message.GetEvent().Data["missing"])
		missing += n
	}
	if missing != 4 {
		t.Fatal("lost queued or post-restart gap", missing, pending)
	}
	ch, done := restarted.SubscribeFor(Filter{}, "edge-a")
	defer done()
	if err = restarted.Publish(gapTelemetry("after-reconnect")); err != nil {
		t.Fatal(err)
	}
	<-ch
	if err = journal.FlushGaps(ctx, true); err != nil {
		t.Fatal(err)
	}
	after, err := journal.Pending(ctx, 100)
	if err != nil || len(after) != len(pending) {
		t.Fatal(after, err)
	}
}

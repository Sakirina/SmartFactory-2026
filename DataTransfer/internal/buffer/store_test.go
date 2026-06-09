package buffer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
)

func TestStoreEnqueueClaimCompleteAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "buffer.db")
	store := openTestStore(t, path)

	msg := testMessage("msg-1")
	if _, err := store.Enqueue(ctx, msg); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if _, err := store.Enqueue(ctx, msg); err != nil {
		t.Fatalf("duplicate Enqueue returned error: %v", err)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats returned error: %v", err)
	}
	if stats.Pending != 1 {
		t.Fatalf("pending = %d, want 1", stats.Pending)
	}

	records, err := store.ClaimPending(ctx, 10, time.Minute)
	if err != nil {
		t.Fatalf("ClaimPending returned error: %v", err)
	}
	if len(records) != 1 || records[0].MessageID != "msg-1" {
		t.Fatalf("records = %+v, want msg-1", records)
	}
	if err := store.MarkCompleted(ctx, []int64{records[0].ID}); err != nil {
		t.Fatalf("MarkCompleted returned error: %v", err)
	}
	stats, err = store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats after completed returned error: %v", err)
	}
	if stats.Completed != 1 {
		t.Fatalf("completed = %d, want 1", stats.Completed)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	reopened := openTestStore(t, path)
	defer reopened.Close()
	stats, err = reopened.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats after reopen returned error: %v", err)
	}
	if stats.Completed != 1 {
		t.Fatalf("completed after reopen = %d, want 1", stats.Completed)
	}
}

func TestStoreLeaseExpiryAndFailedRetry(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "buffer.db"))
	defer store.Close()
	if _, err := store.Enqueue(ctx, testMessage("msg-lease")); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}

	records, err := store.ClaimPending(ctx, 1, time.Millisecond)
	if err != nil {
		t.Fatalf("ClaimPending returned error: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	again, err := store.ClaimPending(ctx, 1, time.Minute)
	if err != nil {
		t.Fatalf("ClaimPending after lease returned error: %v", err)
	}
	if len(again) != 1 || again[0].ID != records[0].ID {
		t.Fatalf("lease claim = %+v, want same record", again)
	}
	if err := store.MarkFailed(ctx, []int64{again[0].ID}, context.DeadlineExceeded); err != nil {
		t.Fatalf("MarkFailed returned error: %v", err)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats returned error: %v", err)
	}
	if stats.Pending != 1 || stats.Retry != 1 || stats.LastErrorCount != 1 {
		t.Fatalf("stats = %+v, want pending=1 retry=1 last_error=1", stats)
	}
}

func TestStoreCleanupTTLAndCapacity(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "buffer.db"))
	defer store.Close()

	freshDone := testMessage("msg-fresh-completed")
	if _, err := store.Enqueue(ctx, freshDone); err != nil {
		t.Fatalf("Enqueue fresh completed returned error: %v", err)
	}
	records, err := store.ClaimPending(ctx, 1, time.Minute)
	if err != nil {
		t.Fatalf("ClaimPending fresh completed returned error: %v", err)
	}
	if err := store.MarkCompleted(ctx, []int64{records[0].ID}); err != nil {
		t.Fatalf("MarkCompleted fresh returned error: %v", err)
	}

	old := testMessage("msg-old")
	old.Timestamp = time.Now().Add(-2 * time.Hour).UnixMilli()
	if _, err := store.Enqueue(ctx, old); err != nil {
		t.Fatalf("Enqueue old returned error: %v", err)
	}
	if err := store.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup returned error: %v", err)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats after cleanup returned error: %v", err)
	}
	if stats.Pending != 0 || stats.Completed != 1 || stats.Dropped != 1 {
		t.Fatalf("stats after TTL cleanup = %+v, want pending=0 completed=1 dropped=1", stats)
	}

	bigStore := openTestStoreWithConfig(t, filepath.Join(t.TempDir(), "capacity.db"), config.BufferConfig{
		Enabled:                true,
		StorageType:            "sqlite",
		Path:                   filepath.Join(t.TempDir(), "capacity.db"),
		MaxSizeMB:              1,
		TTLHours:               168,
		ResumeRateLimit:        1000,
		ResumeBatchSize:        100,
		CleanupIntervalSeconds: 60,
	})
	defer bigStore.Close()
	if _, err := bigStore.Enqueue(ctx, bigMessage("msg-big-1")); err != nil {
		t.Fatalf("Enqueue big 1 returned error: %v", err)
	}
	if _, err := bigStore.Enqueue(ctx, bigMessage("msg-big-2")); err != nil {
		t.Fatalf("Enqueue big 2 returned error: %v", err)
	}
	stats, err = bigStore.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats after capacity returned error: %v", err)
	}
	if stats.Pending != 1 || stats.Dropped == 0 {
		t.Fatalf("capacity stats = %+v, want one pending and dropped > 0", stats)
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	return openTestStoreWithConfig(t, path, config.BufferConfig{
		Enabled:                true,
		StorageType:            "sqlite",
		Path:                   path,
		MaxSizeMB:              512,
		TTLHours:               1,
		ResumeRateLimit:        1000,
		ResumeBatchSize:        100,
		CleanupIntervalSeconds: 60,
	})
}

func openTestStoreWithConfig(t *testing.T, path string, cfg config.BufferConfig) *Store {
	t.Helper()
	cfg.Path = path
	store, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	return store
}

func testMessage(id string) *dtv1.DeviceMessage {
	return &dtv1.DeviceMessage{
		MessageId: id,
		Timestamp: time.Now().UnixMilli(),
		Direction: dtv1.Direction_UPSTREAM,
		Device: &dtv1.DeviceIdentity{
			DeviceId:    "device-1",
			ConnectorId: "connector-1",
		},
		Type: dtv1.MessageType_TELEMETRY,
		Payload: &dtv1.DeviceMessage_Telemetry{
			Telemetry: &dtv1.TelemetryPayload{},
		},
	}
}

func bigMessage(id string) *dtv1.DeviceMessage {
	msg := testMessage(id)
	msg.Metadata = map[string]string{"padding": strings.Repeat("x", 700*1024)}
	return msg
}

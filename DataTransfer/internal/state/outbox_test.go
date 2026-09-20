package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/proto"
)

func TestOutboxRequiresMatchingBusinessAcknowledgementAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "outbox.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	msg := &dtv1.DeviceMessage{MessageId: "event-1", Type: dtv1.MessageType_EVENT, Direction: dtv1.Direction_UPSTREAM, Timestamp: 123}
	if err := store.Enqueue(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(ctx, msg); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pending, err := store.Pending(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	msg.Timestamp++
	if err := store.Enqueue(ctx, msg); !errors.Is(err, ErrConflict) {
		t.Fatalf("content collision: %v", err)
	}
	badAck := &dtv1.MessageAcknowledgement{MessageId: "event-1", PayloadSha256: make([]byte, 32), ReceiverId: "edge-1"}
	if err := store.Acknowledge(ctx, badAck); err == nil {
		t.Fatal("acknowledged a different payload")
	}
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(pending[0])
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	badAck.PayloadSha256 = hash[:]
	if err := store.Acknowledge(ctx, badAck); err != nil {
		t.Fatal(err)
	}
	if err := store.Acknowledge(ctx, badAck); err != nil {
		t.Fatal("duplicate ACK must be idempotent:", err)
	}
	var payloadBytes int
	if err := store.db.QueryRow(`SELECT length(payload) FROM durable_outbox WHERE message_id='event-1'`).Scan(&payloadBytes); err != nil || payloadBytes != 0 {
		t.Fatal("acknowledged transport payload was retained", payloadBytes, err)
	}
	if err := store.Enqueue(ctx, pending[0]); err != nil {
		t.Fatal("identical replay after payload retirement", err)
	}
	if err := store.Enqueue(ctx, msg); !errors.Is(err, ErrConflict) {
		t.Fatal("retired payload lost conflict protection", err)
	}
	pending, err = store.Pending(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("acknowledged outbox = %v, %v", pending, err)
	}
}

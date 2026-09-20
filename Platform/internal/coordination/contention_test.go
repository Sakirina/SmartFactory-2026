package coordination

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/store"
	"github.com/nats-io/nats.go"
)

type absentLeaseStream struct{ nats.JetStreamContext }

func (absentLeaseStream) GetLastMsg(string, string, ...nats.JSOpt) (*nats.RawStreamMsg, error) {
	return nil, nats.ErrMsgNotFound
}

type fenceConflictKV struct{ nats.KeyValue }

func (fenceConflictKV) Bucket() string                        { return "test_lease" }
func (fenceConflictKV) Create(string, []byte) (uint64, error) { return 41, nil }
func (fenceConflictKV) Update(string, []byte, uint64) (uint64, error) {
	return 0, &nats.APIError{Code: 400, ErrorCode: nats.JSErrCodeStreamWrongLastSequence, Description: "wrong last sequence"}
}

func TestFenceCommitContentionDoesNotAuthorizeDegradedActions(t *testing.T) {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{
		Store: &store.Store{NodeID: "edge-b", SignKey: private, Now: time.Now},
		JS:    absentLeaseStream{}, Leases: fenceConflictKV{}, LeaseTTL: 5 * time.Second,
	}
	fence, release, err := c.Acquire(context.Background(), "contended", "edge-b", time.Second)
	if !errors.Is(err, control.ErrLeaseHeld) || fence != 0 || release != nil {
		t.Fatal("fence contention was exposed as a site outage", fence, err)
	}
}

package datatransfer

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"context"
	"errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	dt.UnimplementedDataTransferServiceServer
	acks atomic.Int64
}

func (f *fixture) AcknowledgeMessage(_ context.Context, a *dt.MessageAcknowledgement) (*dt.AcknowledgementResponse, error) {
	if len(a.PayloadSha256) != 32 {
		return nil, errors.New("hash missing")
	}
	if f.acks.Add(1) == 1 {
		return nil, status.Error(codes.Unavailable, "injected acknowledgement loss")
	}
	return &dt.AcknowledgementResponse{Success: true}, nil
}
func TestCommitBeforeAcknowledgementAndReplay(t *testing.T) {
	ctx := context.Background()
	s, e := store.Open(ctx, ":memory:", "edge-a", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.ForwardObservations = true
	_, e = s.Put(ctx, "entity", "counter", 0, model.Entity{ID: "counter", Kind: "device", Status: "approved", EdgeID: "edge-a", Version: 1})
	if e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	server := grpc.NewServer()
	f := &fixture{}
	dt.RegisterDataTransferServiceServer(server, f)
	go server.Serve(listener)
	defer server.Stop()
	bridge, e := Open(s, "edge-a", listener.Addr().String(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer bridge.Close()
	message := &dt.DeviceMessage{MessageId: "event-1", Type: dt.MessageType_EVENT, Device: &dt.DeviceIdentity{DeviceId: "counter"}, Timestamp: time.Now().UnixMilli(), Payload: &dt.DeviceMessage_Event{Event: &dt.EventPayload{EventType: "count", Data: map[string]string{"pulse": "9007199254740993"}}}}
	result, e := bridge.Accept(ctx, message)
	if e == nil || !result.Committed {
		t.Fatalf("lost acknowledgement = %+v %v", result, e)
	}
	result, e = bridge.Accept(ctx, message)
	if e != nil || !result.Duplicate {
		t.Fatalf("replay = %+v %v", result, e)
	}
	point, e := s.Latest(ctx, "counter", "pulse")
	if e != nil || point.Value.(interface{ String() string }).String() != "9007199254740993" {
		t.Fatalf("precision: %+v %v", point, e)
	}
	events, _ := s.List(ctx, "event")
	deliveries, _ := s.Deliveries(ctx, "cloud_observation", 10)
	if len(events) != 1 || len(deliveries) != 1 {
		t.Fatalf("duplicate business effects events=%d forward=%d", len(events), len(deliveries))
	}
	s.Close()
	message.MessageId = "event-2"
	if result, e = bridge.Accept(ctx, message); e == nil || result.Committed {
		t.Fatal("closed database accepted message")
	}
	if f.acks.Load() != 2 {
		t.Fatal("storage failure acknowledged")
	}
}

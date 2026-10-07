package command

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/proto"
)

func (s *Service) CommandResult(ctx context.Context, id string) (*dt.CommandResult, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: command_id is required", ErrInvalidCommand)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.journal != nil {
		return s.journal.CommandResult(ctx, id)
	}
	out := &dt.CommandResult{CommandId: id, Source: "datatransfer.command_memory"}
	entry, ok := s.records[id]
	if !ok || time.Now().After(entry.expiresAt) && entry.status != "running" && entry.status != "accepted" {
		return out, nil
	}
	out.Found = true
	out.Phase = entry.status
	out.RecordedAtMs = entry.updatedAt.UnixMilli()
	out.Response = proto.Clone(entry.response).(*dt.CommandResponsePayload)
	msg := entry.request
	raw, err := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	out.RequestSha256 = sum[:]
	out.DeviceId = msg.GetDevice().GetDeviceId()
	out.Action = msg.GetControl().GetAction()
	out.Params = copyMap(msg.GetControl().GetParams())
	out.BindingKnown = msg.CommandId == id && out.DeviceId != "" && msg.Type == dt.MessageType_CONTROL
	return out, nil
}

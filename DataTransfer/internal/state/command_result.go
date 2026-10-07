package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/proto"
)

// CommandResult reads the existing durable record and never reserves, completes
// or dispatches a command. Legacy digests and responses remain unchanged.
func (s *Store) CommandResult(ctx context.Context, id string) (*dt.CommandResult, error) {
	out := &dt.CommandResult{CommandId: id, Source: "datatransfer.command_journal"}
	var response, request []byte
	err := s.db.QueryRowContext(ctx, `SELECT c.digest,c.response,c.phase,c.updated_at_ms,r.request FROM commands c LEFT JOIN command_requests r ON r.command_id=c.command_id WHERE c.command_id=?`, id).Scan(&out.RequestSha256, &response, &out.Phase, &out.RecordedAtMs, &request)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.Found = true
	out.Response = &dt.CommandResponsePayload{}
	if err = proto.Unmarshal(response, out.Response); err != nil {
		return nil, err
	}
	if len(request) > 0 {
		var msg dt.DeviceMessage
		if err = proto.Unmarshal(request, &msg); err != nil {
			return nil, err
		}
		out.DeviceId = msg.GetDevice().GetDeviceId()
		out.Action = msg.GetControl().GetAction()
		out.Params = msg.GetControl().GetParams()
		hash, err := digest(&msg)
		if err != nil {
			return nil, err
		}
		out.BindingKnown = msg.CommandId == id && out.Response.CommandId == id && out.DeviceId != "" && msg.Type == dt.MessageType_CONTROL && bytes.Equal(hash, out.RequestSha256)
	}
	return out, nil
}

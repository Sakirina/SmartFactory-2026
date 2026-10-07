package datatransfer

import (
	"context"
	"encoding/hex"
	"errors"
	"maps"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (b *Bridge) LookupCommand(ctx context.Context, step model.Step, id string) (control.CommandFeedback, error) {
	if step.EdgeID != b.NodeID {
		return control.CommandFeedback{}, errors.New("command results must be read from their owning edge")
	}
	if _, err := b.entity(ctx, step.DeviceID); err != nil {
		return control.CommandFeedback{}, err
	}
	result, err := b.Client.GetCommandResult(ctx, &dt.CommandResultRequest{CommandId: id})
	if err != nil {
		return control.CommandFeedback{}, err
	}
	feedback := control.CommandFeedback{CommandID: result.CommandId, DeviceID: result.DeviceId, Source: result.Source + ":" + b.NodeID, ObservedMS: result.RecordedAtMs, Found: result.Found, RequestHash: hex.EncodeToString(result.RequestSha256), ContentHash: store.Hash(result)}
	feedback.Bound = result.BindingKnown && result.CommandId == id && result.DeviceId == step.DeviceID && result.Action == step.Action && maps.Equal(result.Params, step.Params)
	if feedback.Bound {
		feedback.PayloadHash = store.Hash(step)
	}
	if result.Response != nil {
		feedback.Status = result.Response.Status.String()
		feedback.Message = result.Response.Message
		feedback.Bound = feedback.Bound && result.Response.CommandId == id
	}
	return feedback, nil
}

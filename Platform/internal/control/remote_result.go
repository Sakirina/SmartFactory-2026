package control

import (
	"context"
	"errors"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// QueryRemoteResult is invoked only after verifying the site request signature.
// The current lease permits reading an older command journal without sending
// another physical command or changing the owner's execution state.
func (s *Service) QueryRemoteResult(ctx context.Context, req model.Execution, step model.Step, commandID string) (CommandFeedback, error) {
	if !s.Edge || step.EdgeID != s.NodeID || req.Fence == 0 || req.CoordinatorID == "" || commandID != req.DownlinkID+":"+step.ID {
		return CommandFeedback{}, errors.New("invalid coordinated result query")
	}
	c, ok := s.Coordinator.(ExecutionCoordinator)
	if !ok {
		return CommandFeedback{}, errors.New("coordination fencing is unavailable")
	}
	if err := c.Validate(ctx, req.DownlinkID, req.CoordinatorID, req.Fence); err != nil {
		return CommandFeedback{}, err
	}
	global, err := c.Recover(ctx, req.DownlinkID)
	if err != nil {
		return CommandFeedback{}, err
	}
	if !sameRequest(global, req) || global.Fence > req.Fence {
		return CommandFeedback{}, store.ErrConflict
	}
	d, err := s.Definitions.Version(ctx, req.DefinitionID, req.DefinitionVersion)
	if err != nil {
		return CommandFeedback{}, err
	}
	expected, err := stepForAction(d, req, commandID)
	if err != nil || store.Hash(expected) != store.Hash(step) {
		return CommandFeedback{}, store.ErrConflict
	}
	doc, err := s.Store.Get(ctx, "entity", step.DeviceID)
	if err != nil {
		return CommandFeedback{}, err
	}
	device, err := store.Decode[model.Entity](doc)
	if err != nil {
		return CommandFeedback{}, err
	}
	if device.EdgeID != s.NodeID {
		return CommandFeedback{}, errors.New("result query target is not owned locally")
	}
	if query, ok := s.Dispatcher.(ResultQuerier); ok {
		return query.LookupCommand(ctx, step, commandID)
	}
	// Older deployments may retain a confirmed Platform action journal even
	// when the protocol adapter has no result-query capability.
	action, err := s.Store.ControlAction(ctx, commandID)
	if errors.Is(err, store.ErrNotFound) {
		return CommandFeedback{CommandID: commandID, DeviceID: step.DeviceID}, nil
	}
	if err != nil {
		return CommandFeedback{}, err
	}
	return CommandFeedback{CommandID: commandID, DeviceID: step.DeviceID, Found: true, Bound: action.PayloadHash == store.Hash(step), PayloadHash: action.PayloadHash, RequestHash: action.PayloadHash, Source: s.NodeID + ":action_journal", ObservedMS: action.Result.FinishedMS, Status: action.Result.Status, Message: action.Result.Message, ContentHash: store.Hash(action)}, nil
}

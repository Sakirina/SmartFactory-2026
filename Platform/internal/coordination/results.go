package coordination

import (
	"context"
	"errors"
	"time"

	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/pkg/model"
	"github.com/nats-io/nats.go"
)

type resultResponse struct {
	CommandID string                  `json:"command_id"`
	Result    control.CommandFeedback `json:"result"`
	Error     string                  `json:"error,omitempty"`
}

func (d *Dispatcher) LookupCommand(ctx context.Context, step model.Step, id string) (control.CommandFeedback, error) {
	c := d.Coordinator
	if step.EdgeID == c.Store.NodeID {
		query, ok := d.Local.(control.ResultQuerier)
		if !ok {
			return control.CommandFeedback{}, errors.New("local read-only result query is unavailable")
		}
		return query.LookupCommand(ctx, step, id)
	}
	req, ok := control.ExecutionFromContext(ctx)
	if !ok || req.Fence == 0 {
		return control.CommandFeedback{}, errors.New("cross-edge result query requires a fenced execution")
	}
	if err := c.Validate(ctx, req.DownlinkID, req.CoordinatorID, req.Fence); err != nil {
		return control.CommandFeedback{}, err
	}
	deadline := c.Store.Now().Add(5 * time.Second).UnixMilli()
	if limit, ok := ctx.Deadline(); ok && limit.UnixMilli() < deadline {
		deadline = limit.UnixMilli()
	}
	payload, err := c.pack(StepRequest{Execution: req, Step: step, CommandID: id, AtMS: c.Store.Now().UnixMilli(), DeadlineMS: deadline})
	if err != nil {
		return control.CommandFeedback{}, err
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	message, err := c.Connection.RequestWithContext(call, c.Prefix+".result."+key(step.EdgeID), payload)
	if err != nil {
		return control.CommandFeedback{}, err
	}
	var response resultResponse
	signer, err := c.unpack(ctx, message.Data, &response)
	if err != nil {
		return control.CommandFeedback{}, err
	}
	if signer != step.EdgeID || response.CommandID != id {
		return control.CommandFeedback{}, errors.New("command result identity mismatch")
	}
	if response.Error != "" {
		return control.CommandFeedback{}, errors.New(response.Error)
	}
	return response.Result, nil
}

func (c *Coordinator) serveResults(ctx context.Context, service *control.Service) (*nats.Subscription, error) {
	return c.Connection.Subscribe(c.Prefix+".result."+key(c.Store.NodeID), func(message *nats.Msg) {
		var request StepRequest
		response := resultResponse{}
		signer, err := c.unpack(ctx, message.Data, &request)
		response.CommandID = request.CommandID
		if err == nil && (signer != request.Execution.CoordinatorID || request.Step.EdgeID != c.Store.NodeID) {
			err = errors.New("result query sender is not the current coordinator")
		}
		now := c.Store.Now().UnixMilli()
		if err == nil && (request.DeadlineMS <= now || request.AtMS > now+5000 || now-request.AtMS > 15000) {
			err = errors.New("site result query expired")
		}
		if err == nil {
			call, cancel := context.WithDeadline(ctx, time.UnixMilli(request.DeadlineMS))
			defer cancel()
			response.Result, err = service.QueryRemoteResult(call, request.Execution, request.Step, request.CommandID)
		}
		if err != nil {
			response.Error = err.Error()
		}
		if raw, err := c.pack(response); err == nil {
			_ = message.Respond(raw)
		}
	})
}

func (m *Manager) LookupCommand(ctx context.Context, step model.Step, id string) (control.CommandFeedback, error) {
	if step.EdgeID == m.Store.NodeID {
		query, ok := m.Local.(control.ResultQuerier)
		if !ok {
			return control.CommandFeedback{}, errors.New("local read-only result query is unavailable")
		}
		return query.LookupCommand(ctx, step, id)
	}
	c, err := m.active()
	if err != nil {
		return control.CommandFeedback{}, err
	}
	return (&Dispatcher{Coordinator: c, Local: m.Local}).LookupCommand(ctx, step, id)
}

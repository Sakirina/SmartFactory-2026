package coordination

import (
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/pkg/model"
	"context"
	"errors"
	"fmt"
	"github.com/nats-io/nats.go"
	"time"
)

type StepRequest struct {
	Execution  model.Execution `json:"execution"`
	Step       model.Step      `json:"step"`
	CommandID  string          `json:"command_id"`
	DeadlineMS int64           `json:"deadline_ms"`
	AtMS       int64           `json:"at_ms"`
}
type StepResponse struct {
	CommandID string           `json:"command_id"`
	Result    model.StepResult `json:"result"`
	Error     string           `json:"error,omitempty"`
}
type Dispatcher struct {
	Coordinator *Coordinator
	Local       control.Dispatcher
}

func (d *Dispatcher) Send(ctx context.Context, step model.Step, id string, deadline int64) (control.DispatchResult, error) {
	c := d.Coordinator
	if step.EdgeID == c.Store.NodeID {
		if d.Local == nil {
			return control.DispatchResult{}, errors.New("local protocol dispatcher is unavailable")
		}
		return d.Local.Send(ctx, step, id, deadline)
	}
	req, ok := control.ExecutionFromContext(ctx)
	if !ok || req.Fence == 0 {
		return control.DispatchResult{}, errors.New("cross-edge command requires a fenced execution")
	}
	if e := c.Validate(ctx, req.DownlinkID, req.CoordinatorID, req.Fence); e != nil {
		return control.DispatchResult{}, e
	}
	payload, e := c.pack(StepRequest{Execution: req, Step: step, CommandID: id, DeadlineMS: deadline, AtMS: c.Store.Now().UnixMilli()})
	if e != nil {
		return control.DispatchResult{}, e
	}
	message, e := c.Connection.RequestWithContext(ctx, c.Prefix+".control."+key(step.EdgeID), payload)
	if e != nil {
		return control.DispatchResult{}, e
	}
	var response StepResponse
	signer, e := c.unpack(ctx, message.Data, &response)
	if e != nil {
		return control.DispatchResult{}, e
	}
	if signer != step.EdgeID || response.CommandID != id {
		return control.DispatchResult{}, errors.New("device response identity mismatch")
	}
	if response.Error != "" {
		return control.DispatchResult{}, errors.New(response.Error)
	}
	return control.DispatchResult{Status: response.Result.Status, Message: response.Result.Message}, nil
}
func (c *Coordinator) ServeCommands(ctx context.Context, service *control.Service) error {
	subscription, e := c.Connection.Subscribe(c.Prefix+".control."+key(c.Store.NodeID), func(message *nats.Msg) {
		var request StepRequest
		response := StepResponse{}
		signer, err := c.unpack(ctx, message.Data, &request)
		response.CommandID = request.CommandID
		if err == nil && (signer != request.Execution.CoordinatorID || request.Step.EdgeID != c.Store.NodeID) {
			err = errors.New("command sender is not the execution coordinator")
		}
		now := c.Store.Now().UnixMilli()
		if err == nil && (request.DeadlineMS <= now || request.AtMS > now+5000 || now-request.AtMS > 15000) {
			err = errors.New("site command expired")
		}
		if err == nil {
			call, cancel := context.WithDeadline(ctx, time.UnixMilli(request.DeadlineMS))
			defer cancel()
			response.Result, err = service.ExecuteRemoteStep(call, request.Execution, request.Step, request.CommandID)
		}
		if err != nil {
			response.Error = err.Error()
		}
		raw, err := c.pack(response)
		if err == nil {
			_ = message.Respond(raw)
		}
	})
	if e != nil {
		return e
	}
	defer subscription.Unsubscribe()
	flush, done := context.WithTimeout(ctx, 3*time.Second)
	e = c.Connection.FlushWithContext(flush)
	done()
	if e != nil {
		return e
	}
	<-ctx.Done()
	return nil
}
func (c *Coordinator) Resume(ctx context.Context, service *control.Service) error {
	executions, e := c.Pending(ctx)
	if e != nil {
		return e
	}
	for _, execution := range executions {
		definition, e := service.Definitions.Version(ctx, execution.DefinitionID, execution.DefinitionVersion)
		if e != nil {
			return e
		}
		participant := false
		for _, node := range definition.Policy.EdgeIDs {
			participant = participant || node == c.Store.NodeID
		}
		if !participant {
			continue
		}
		_, e = service.Run(ctx, execution, execution.Actor.UserID == "published-policy")
		if errors.Is(e, control.ErrLeaseHeld) {
			continue
		}
		if e != nil {
			return fmt.Errorf("resume execution %s: %w", execution.DownlinkID, e)
		}
	}
	return nil
}

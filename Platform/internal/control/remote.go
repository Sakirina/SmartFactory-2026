package control

import (
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"context"
	"errors"
	"strings"
)

// ExecuteRemoteStep validates the published plan and local interlocks before
// reserving a device action in the target owner's durable journal.
func (s *Service) ExecuteRemoteStep(ctx context.Context, req model.Execution, step model.Step, commandID string) (model.StepResult, error) {
	if !s.Edge || step.EdgeID != s.NodeID || req.Fence == 0 || req.CoordinatorID == "" || req.Status != "running" {
		return model.StepResult{}, errors.New("invalid coordinated device step")
	}
	if commandID != req.DownlinkID+":"+step.ID {
		return model.StepResult{}, errors.New("command identifier does not match the execution")
	}
	coordinator, ok := s.Coordinator.(ExecutionCoordinator)
	if !ok {
		return model.StepResult{}, errors.New("coordination fencing is unavailable")
	}
	if e := coordinator.Validate(ctx, req.DownlinkID, req.CoordinatorID, req.Fence); e != nil {
		return model.StepResult{}, e
	}
	global, e := coordinator.Recover(ctx, req.DownlinkID)
	if e != nil {
		return model.StepResult{}, e
	}
	if global.Binding != req.Binding || global.DefinitionID != req.DefinitionID || global.DefinitionVersion != req.DefinitionVersion || global.Status != "running" || global.Fence != req.Fence || global.CoordinatorID != req.CoordinatorID || store.Hash(global.Params) != store.Hash(req.Params) || global.Override != req.Override {
		return model.StepResult{}, errors.New("execution journal does not authorize this step")
	}
	req = global
	definition, e := s.Definitions.Version(ctx, req.DefinitionID, req.DefinitionVersion)
	if e != nil {
		return model.StepResult{}, e
	}
	found := false
	for _, candidate := range definition.Policy.Steps {
		if candidate.ID == step.ID {
			candidate.Params = bindParams(candidate.Params, req.Params)
			found = store.Hash(candidate) == store.Hash(step)
		}
	}
	if !found {
		return model.StepResult{}, errors.New("device step differs from the published policy")
	}
	doc, e := s.Store.Get(ctx, "entity", step.DeviceID)
	if e != nil {
		return model.StepResult{}, e
	}
	device, e := store.Decode[model.Entity](doc)
	if e != nil {
		return model.StepResult{}, e
	}
	if device.EdgeID != s.NodeID || device.Status != "approved" {
		return model.StepResult{}, errors.New("target is not an approved local device")
	}
	// The coordinator validates cross-node preconditions. The owner independently
	// refreshes every condition concerning its own devices before the physical write.
	local := definition
	local.Policy.Conditions = nil
	for _, condition := range definition.Policy.Conditions {
		d, e := s.Store.Get(ctx, "entity", condition.DeviceID)
		if e != nil {
			return model.StepResult{}, e
		}
		v, e := store.Decode[model.Entity](d)
		if e != nil {
			return model.StepResult{}, e
		}
		if v.EdgeID == s.NodeID {
			local.Policy.Conditions = append(local.Policy.Conditions, condition)
		}
	}
	check, e := s.Conditions(ctx, local)
	if e != nil {
		return model.StepResult{}, e
	}
	if check.Interlocked || !check.Allowed && !req.Override {
		return model.StepResult{StepID: step.ID, CommandID: commandID, Status: "REJECTED", Message: strings.Join(check.Reasons, "; ")}, nil
	}
	result, e := s.runStep(ctx, req, step, commandID)
	if e == nil {
		e = s.Store.Audit(ctx, req.Actor, "control.remote_step", step.DeviceID, req.DownlinkID, map[string]any{"execution": req, "step": step, "result": result, "snapshot": check.Snapshot})
	}
	return result, e
}

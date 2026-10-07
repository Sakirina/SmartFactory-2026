package control

import (
	"errors"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// ValidateReceiptEvidence checks the immutable published command, including its
// bound parameters. Transport authentication binds the enclosing source metadata.
func ValidateReceiptEvidence(req model.Execution, definition model.Definition, item model.CommandEvidence) error {
	if item.ID == "" || item.ExecutionID != req.DownlinkID || item.CollectedMS <= 0 || item.CollectedMS < req.CreatedMS {
		return errors.New("invalid execution evidence identity or collection time")
	}
	step, err := stepForAction(definition, req, item.CommandID)
	if err != nil {
		return err
	}
	if item.StepID != step.ID || item.DeviceID != step.DeviceID || item.PayloadHash != store.Hash(step) {
		return errors.New("execution evidence does not match its original command payload")
	}
	if item.Trusted {
		if item.Rejection != "" || item.Source == "" || item.ContentHash == "" || item.ObservedMS <= 0 || item.ObservedMS > item.CollectedMS+5000 || !has([]string{"SUCCESS", "FAILURE", "REJECTED"}, item.Status) {
			return errors.New("trusted execution evidence has invalid provenance")
		}
		for _, result := range req.Steps {
			if result.CommandID == item.CommandID && (item.ObservedMS < result.StartedMS || (result.EvidenceID == item.ID && result.Status != item.Status)) {
				return errors.New("execution evidence conflicts with its command result")
			}
		}
	} else if item.Rejection == "" {
		return errors.New("untrusted execution evidence requires its rejection reason")
	}
	return nil
}

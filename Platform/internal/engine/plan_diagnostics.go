package engine

import (
	"context"
	"errors"

	"competition2026/product/platform/internal/compiledplan"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type PlanDiagnostic struct {
	DefinitionID      string     `json:"definition_id"`
	DefinitionVersion int64      `json:"definition_version"`
	PlanID            string     `json:"plan_id"`
	PlanSHA256        string     `json:"plan_sha256,omitempty"`
	ContentSHA256     string     `json:"content_sha256"`
	Status            string     `json:"status"`
	Issue             *PlanIssue `json:"issue,omitempty"`
}

// PlanDiagnostics is read-only. The API checks every historical definition and
// reference before calling it, including references of isolated legacy plans.
func (s *Service) PlanDiagnostics(ctx context.Context, id string) ([]PlanDiagnostic, error) {
	result := []PlanDiagnostic{}
	documents, err := s.Store.Versions(ctx, "definition", id)
	if err != nil {
		return nil, err
	}
	for _, document := range documents {
		definition, err := store.Decode[model.Definition](document)
		if err != nil {
			return nil, err
		}
		if definition.Status != "published" {
			continue
		}
		content, err := rulecore.ContentHash(definition)
		if err != nil {
			return nil, err
		}
		diagnostic := PlanDiagnostic{DefinitionID: definition.ID, DefinitionVersion: definition.Version, PlanID: rulecore.PlanID(definition, content), ContentSHA256: content, Status: "unprepared"}
		issueDoc, err := s.Store.Get(ctx, "plan_issue", diagnostic.PlanID)
		if err == nil {
			issue, err := store.Decode[PlanIssue](issueDoc)
			if err != nil {
				return nil, err
			}
			diagnostic.Status, diagnostic.Issue = "isolated", &issue
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		plan, err := compiledplan.Load(ctx, s.Store, definition)
		if err == nil {
			diagnostic.Status, diagnostic.PlanSHA256 = "prepared", plan.SHA256
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		result = append(result, diagnostic)
	}
	return result, nil
}

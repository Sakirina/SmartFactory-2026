package engine

import (
	"context"
	"errors"

	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *Service) prepareFrame(ctx context.Context, definition model.Definition, plan *model.ExecutionPlan, point model.Observation, historical bool, states map[string]RuntimeState) (rulecore.Frame, error) {
	frame := rulecore.Frame{Observation: point, EntityID: point.DeviceID, ClockMS: point.ObservedMS, FreshnessAtMS: s.Store.Now().UnixMilli(), InitialState: states, InputFields: map[string]model.Observation{}, AssetVersions: []model.AssetVersion{}, Historical: historical, FreshnessMS: 5000}
	if historical {
		frame.FreshnessAtMS = point.ObservedMS
	}
	if at, ok := ctx.Value(timerAtKey{}).(int64); ok {
		frame.ClockMS, frame.FreshnessAtMS = at, at
	}
	if definition.Selector.AssetID != "" {
		frame.EntityID = definition.Selector.AssetID
	}
	for id, depth := point.DeviceID, 0; id != "" && depth < 64; depth++ {
		document, err := s.Store.VersionAt(ctx, "entity", id, point.ObservedMS)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return frame, err
		}
		entity, err := store.Decode[model.Entity](document)
		if err != nil {
			return frame, err
		}
		frame.AssetVersions = append(frame.AssetVersions, model.AssetVersion{ID: id, Version: document.Version, EffectiveMS: document.UpdatedMS, ParentID: entity.ParentID, SamplingMS: entity.SamplingMS})
		if id == point.DeviceID && entity.SamplingMS > frame.FreshnessMS/3 {
			frame.FreshnessMS = entity.SamplingMS * 3
		}
		id = entity.ParentID
	}
	if definition.Policy.FreshnessMS > 0 {
		frame.FreshnessMS = definition.Policy.FreshnessMS
	}
	var err error
	frame.Window, err = s.window(ctx, definition, point)
	if err != nil {
		return frame, err
	}
	sortReplay(frame.Window)
	for _, node := range plan.Nodes {
		key := node.Params.Key
		if node.Type != "input" || key == "" || key == point.Key {
			continue
		}
		if _, exists := frame.InputFields[key]; exists {
			continue
		}
		observations, err := s.Store.Query(ctx, store.Query{DeviceIDs: []string{point.DeviceID}, Keys: []string{key}, FromMS: 0, ToMS: point.ObservedMS, Limit: 1})
		if err != nil {
			return frame, err
		}
		if len(observations.Points) > 0 {
			frame.InputFields[key] = observations.Points[0]
		}
	}
	return frame, nil
}

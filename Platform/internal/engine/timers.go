package engine

import (
	"context"
	"errors"
	"fmt"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type timerAtKey struct{}

func (s *Service) freshness(ctx context.Context, d model.Definition, device string) (int64, error) {
	if d.Policy.FreshnessMS > 0 {
		return d.Policy.FreshnessMS, nil
	}
	age := int64(5000)
	doc, err := s.Store.Get(ctx, "entity", device)
	if errors.Is(err, store.ErrNotFound) {
		return age, nil
	}
	if err != nil {
		return 0, err
	}
	entity, err := store.Decode[model.Entity](doc)
	if entity.SamplingMS > age/3 {
		age = entity.SamplingMS * 3
	}
	return age, err
}

// Tick advances pending debounce states and explicit freshness watchdogs using
// their persisted input. Timer evaluations never append synthetic sensor values.
func (s *Service) Tick(ctx context.Context) error {
	s.strategyMu.Lock()
	defer s.strategyMu.Unlock()
	docs, err := s.Store.List(ctx, "definition")
	if err != nil {
		return err
	}
	now := s.Store.Now().UnixMilli()
	for _, doc := range docs {
		d, err := store.Decode[model.Definition](doc)
		if err != nil {
			return err
		}
		if d.Kind != "strategy" || d.Status != "published" {
			continue
		}
		hasTimer := d.Policy.Watchdog
		for _, node := range d.Nodes {
			hasTimer = hasTimer || node.Type == "debounce"
		}
		if !hasTimer {
			continue
		}
		// Explicit device selectors keep timer work bounded and independently owned.
		for _, device := range d.Selector.DeviceIDs {
			entityDoc, err := s.Store.Get(ctx, "entity", device)
			if err != nil {
				return err
			}
			entity, err := store.Decode[model.Entity](entityDoc)
			if err != nil {
				return err
			}
			if entity.EdgeID != "" && entity.EdgeID != s.Store.NodeID {
				continue
			}
			for _, field := range d.Selector.Keys {
				input, err := s.Store.Latest(ctx, device, field)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				stateID := fmt.Sprintf("%s:%d:%s", d.ID, d.Version, device)
				states := map[string]RuntimeState{}
				saved, err := s.Store.Get(ctx, "engine_state", stateID)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				if err = store.DecodeJSON(saved.Data, &states); err != nil {
					return err
				}
				due := int64(0)
				if d.Policy.Watchdog {
					age, err := s.freshness(ctx, d, device)
					if err != nil {
						return err
					}
					if at := input.ObservedMS + age + 1; at <= now {
						due = at
					}
				}
				for _, node := range d.Nodes {
					st := states[node.ID]
					if node.Type == "debounce" && st.SinceMS > 0 && st.Active != st.Candidate {
						at := st.SinceMS + integer(node.Params["duration_ms"], 1000)
						if at <= now && (due == 0 || at < due) {
							due = at
						}
					}
				}
				if due == 0 {
					continue
				}
				id := fmt.Sprintf("timer:%s:%d:%s:%d", d.ID, d.Version, input.ID, due)
				result, err := s.Evaluate(context.WithValue(ctx, timerAtKey{}, now), d, input, false, states)
				if err != nil {
					return err
				}
				if _, err = s.commitEvaluation(ctx, id, "engine_state", stateID, d, input, result); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

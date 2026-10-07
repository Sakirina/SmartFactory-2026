package application

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *History) Shadow(ctx context.Context, p identity.Principal, id string) (historymodel.Candidate, error) {
	doc, err := s.Store.Get(ctx, "shadow_candidate", id)
	if err != nil {
		return historymodel.Candidate{}, err
	}
	candidate, err := store.Decode[historymodel.Candidate](doc)
	if err != nil {
		return candidate, err
	}
	candidate.Version = doc.Version
	if _, err = s.authorize(ctx, p, candidate.Resources, "read"); err != nil {
		return historymodel.Candidate{}, err
	}
	return candidate, nil
}
func (s *History) Shadows(ctx context.Context, p identity.Principal) (historymodel.CandidateList, error) {
	out := historymodel.CandidateList{Items: []historymodel.Candidate{}}
	docs, err := s.Store.List(ctx, "shadow_candidate")
	if err != nil {
		return out, err
	}
	for _, d := range docs {
		c, err := s.Shadow(ctx, p, d.ID)
		if errors.Is(err, identity.ErrDenied) {
			continue
		}
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, c)
	}
	return out, nil
}

func (s *History) EnableShadow(ctx context.Context, p identity.Principal, request historymodel.ShadowRequest) (historymodel.Candidate, error) {
	if request.ID == "" || request.DefinitionID == "" || request.Version < 1 {
		return historymodel.Candidate{}, errors.New("candidate id, definition_id and published version are required")
	}
	now := s.Store.CurrentTime().UnixMilli()
	rules, err := s.rules(ctx, historymodel.Request{Kind: "compare", DefinitionID: request.DefinitionID, LeftVersion: request.Version, RightVersion: request.Version})
	if err != nil {
		return historymodel.Candidate{}, err
	}
	rule := rules[0]
	devices := request.DeviceIDs
	if len(devices) == 0 {
		devices = rule.Definition.Selector.DeviceIDs
	}
	if len(devices) == 0 {
		return historymodel.Candidate{}, errors.New("shadow candidate requires explicit device_ids")
	}
	devices = uniqueHistory(devices)
	keys := request.Keys
	if len(keys) == 0 {
		keys = rule.Definition.Selector.Keys
	}
	keys = uniqueHistory(keys)
	initial := request.InitialState
	initialSource := "provided"
	if initial == nil {
		initial = historymodel.State{}
		initialSource = "empty"
	}
	// Context present at activation is pinned once. Later inputs are recorded by
	// observation identity and revision, so restarting never rereads this base.
	history := []model.Observation{}
	historyKeys := append([]string{}, keys...)
	for _, n := range rule.Plan.Nodes {
		if n.Type == "input" && n.Params.Key != "" {
			historyKeys = append(historyKeys, n.Params.Key)
		}
	}
	historyKeys = uniqueHistory(historyKeys)
	if rule.Definition.Selector.WindowMS > 0 {
		data, err := s.Store.Query(ctx, store.Query{DeviceIDs: devices, Keys: historyKeys, FromMS: max(int64(1), now-rule.Definition.Selector.WindowMS), ToMS: now, Limit: historymodel.MaxHistory})
		if err != nil {
			return historymodel.Candidate{}, err
		}
		if data.Truncated {
			return historymodel.Candidate{}, errors.New("candidate activation history exceeds budget")
		}
		history = data.Points
	}
	for _, device := range devices {
		for _, key := range historyKeys {
			data, err := s.Store.Query(ctx, store.Query{DeviceIDs: []string{device}, Keys: []string{key}, FromMS: 0, ToMS: now, Limit: 1})
			if err != nil {
				return historymodel.Candidate{}, err
			}
			history = append(history, data.Points...)
		}
	}
	// Normalize duplicate context records through the same snapshot validator.
	seen := map[string]bool{}
	filtered := []model.Observation{}
	for _, point := range history {
		if !seen[point.ID] {
			seen[point.ID] = true
			filtered = append(filtered, point)
		}
	}
	history = filtered
	assets, err := s.assets(ctx, history, now)
	if err != nil {
		return historymodel.Candidate{}, err
	}
	resources, err := s.dependencyResources(ctx, rules)
	if err != nil {
		return historymodel.Candidate{}, err
	}
	resources = uniqueHistory(append(append(resources, devices...), historyResources(rules, nil, history, assets, initial)...))
	epoch := int64(1)
	if request.ExpectedVersion > 0 {
		old, err := s.Shadow(ctx, p, request.ID)
		if err != nil {
			return historymodel.Candidate{}, err
		}
		if old.Version != request.ExpectedVersion {
			return historymodel.Candidate{}, store.ErrConflict
		}
		epoch = old.Epoch + 1
		resources = uniqueHistory(append(resources, old.Resources...))
	}
	guard, err := s.authorize(ctx, p, resources, "draft")
	if err != nil {
		return historymodel.Candidate{}, err
	}
	base := historymodel.Snapshot{FromMS: now, ToMS: now, Order: "event_time", InitialState: initial, InitialStateSource: initialSource, Source: "shadow_activation", History: history, HistorySHA256: store.Hash(history), Points: []model.Observation{}, AssetVersions: assets, Rules: rules, Resources: resources, CapturedMS: now}
	base, err = finalizeHistorySnapshot(base)
	if err != nil {
		return historymodel.Candidate{}, err
	}
	candidate := historymodel.Candidate{ID: request.ID, DefinitionID: request.DefinitionID, DefinitionVersion: request.Version, Version: request.ExpectedVersion + 1, Epoch: epoch, Status: "active", DeviceIDs: devices, Keys: keys, Resources: resources, StartedMS: now, Context: history, BaseSnapshot: base}
	return s.Store.SaveShadow(ctx, candidate, request.ExpectedVersion, p.Actor, guard.check)
}

func (s *History) DisableShadow(ctx context.Context, p identity.Principal, id string, expected int64) (historymodel.Candidate, error) {
	c, err := s.Shadow(ctx, p, id)
	if err != nil {
		return c, err
	}
	guard, err := s.authorize(ctx, p, c.Resources, "draft")
	if err != nil {
		return historymodel.Candidate{}, err
	}
	return s.Store.StopShadow(ctx, id, expected, p.Actor, guard.check)
}

// Observe is called before formal processing by the durable engine/strategy
// outbox consumers. Returning an error leaves that input delivery retryable.
// Calculation failures are confined to separately queued analysis workers.
func (s *History) Observe(ctx context.Context, p model.Observation) error {
	if p.ID == "" {
		return errors.New("shadow propagation requires a persisted observation identity")
	}
	docs, err := s.Store.List(ctx, "shadow_candidate")
	if err != nil {
		return err
	}
	for _, doc := range docs {
		candidate, err := store.Decode[historymodel.Candidate](doc)
		if err != nil {
			return err
		}
		candidate.Version = doc.Version
		if candidate.Status != "active" || !includesHistory(candidate.DeviceIDs, p.DeviceID) || len(candidate.Keys) > 0 && !includesHistory(candidate.Keys, p.Key) {
			continue
		}
		for attempt := 0; attempt < 8; attempt++ {
			err = s.observeCandidate(ctx, candidate, p)
			if !errors.Is(err, store.ErrConflict) {
				break
			}
			current, e := s.Store.Get(ctx, "shadow_candidate", candidate.ID)
			if e != nil {
				return e
			}
			candidate, e = store.Decode[historymodel.Candidate](current)
			if e != nil {
				return e
			}
			candidate.Version = current.Version
			if candidate.Status != "active" {
				err = nil
				break
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *History) observeCandidate(ctx context.Context, c historymodel.Candidate, p model.Observation) error {
	if c.Status != "active" || !includesHistory(c.DeviceIDs, p.DeviceID) || len(c.Keys) > 0 && !includesHistory(c.Keys, p.Key) {
		return nil
	}
	known, err := s.Store.ShadowInputKnown(ctx, c.ID, c.Epoch, p)
	if err != nil || known {
		return err
	}
	correction := c.LastInput != nil && (p.Revision > 1 || !shadowBefore(*c.LastInput, p))
	snapshot := c.BaseSnapshot
	snapshot.ID = ""
	snapshot.SHA256 = ""
	snapshot.CapturedMS = s.Store.CurrentTime().UnixMilli()
	snapshot.Source = "shadow_observations"
	snapshot.FromMS = p.ObservedMS
	snapshot.ToMS = p.ObservedMS
	points := []model.Observation{p}
	parent := ""
	if correction {
		points, err = s.Store.ShadowInputs(ctx, c.ID, c.Epoch)
		if err != nil {
			return err
		}
		found := false
		for i, item := range points {
			if engine.AnalysisInputIdentity(item) == engine.AnalysisInputIdentity(p) {
				found = true
				if item.Revision <= p.Revision {
					points[i] = p
				}
				break
			}
		}
		if !found {
			points = append(points, p)
		}
		snapshot.History = append([]model.Observation{}, c.BaseSnapshot.History...)
		snapshot.InitialState = c.BaseSnapshot.InitialState
		snapshot.InitialStateSource = c.BaseSnapshot.InitialStateSource
		snapshot.InitialStateRunID = ""
		c.InputCount = len(points)
	} else {
		c.InputCount++
		snapshot.History = append([]model.Observation{}, c.Context...)
		if c.Generation > 0 {
			parent = fmt.Sprintf("shadow:%s:%d:%d", c.ID, c.Epoch, c.Generation)
			snapshot.InitialState = historymodel.State{}
			snapshot.InitialStateSource = "pending_previous_shadow_run"
			snapshot.InitialStateRunID = parent
		}
	}
	if c.InputCount > historymodel.MaxPoints {
		return s.Store.PauseShadow(ctx, c.ID, c.Version, "candidate input budget exceeded; enable a new candidate epoch")
	}
	snapshot.Points = points
	for _, point := range points {
		snapshot.FromMS = min(snapshot.FromMS, point.ObservedMS)
		snapshot.ToMS = max(snapshot.ToMS, point.ObservedMS)
	}
	snapshot.AssetVersions, err = s.assets(ctx, append(append([]model.Observation{}, points...), snapshot.History...), snapshot.ToMS)
	if err != nil {
		return err
	}
	if !engine.AnalysisMatches(snapshot.Rules[0].Definition, p, snapshot.AssetVersions) {
		return nil
	}
	official, err := s.Store.Get(ctx, "definition", c.DefinitionID)
	if err != nil {
		return err
	}
	d, err := store.Decode[model.Definition](official)
	if err != nil {
		return err
	}
	formalResources, err := s.dependencyResources(ctx, []historymodel.Rule{{Definition: d}})
	if err != nil {
		return err
	}
	snapshot.Resources = uniqueHistory(append(append(append([]string{}, c.Resources...), formalResources...), historyResources(snapshot.Rules, points, snapshot.History, snapshot.AssetVersions, snapshot.InitialState)...))
	if err = engine.NormalizeAnalysis(&snapshot); err != nil {
		return err
	}
	snapshot, err = finalizeHistorySnapshot(snapshot)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("shadow:%s:%d:%d", c.ID, c.Epoch, c.Generation+1)
	run := newHistoryRun(id, "shadow", c.DefinitionID, snapshot)
	run.CandidateID = c.ID
	run.CandidateVersion = c.Epoch
	run.InputGeneration = c.Generation + 1
	run.ParentRunID = parent
	contextPoints := append(append([]model.Observation{}, c.Context...), p)
	if correction {
		contextPoints = append(append([]model.Observation{}, c.BaseSnapshot.History...), points...)
	}
	c.Context = shadowContext(contextPoints, snapshot.Rules[0].Definition.Selector.WindowMS)
	if c.LastInput == nil || shadowBefore(*c.LastInput, p) {
		copy := p
		c.LastInput = &copy
	}
	return s.Store.CaptureShadow(ctx, c, p, run, snapshot)
}

func shadowBefore(a, b model.Observation) bool {
	if a.ObservedMS != b.ObservedMS {
		return a.ObservedMS < b.ObservedMS
	}
	if a.SourceSequence != b.SourceSequence {
		return a.SourceSequence < b.SourceSequence
	}
	return a.ID < b.ID
}
func shadowContext(points []model.Observation, window int64) []model.Observation {
	latestIdentity := map[string]model.Observation{}
	latestField := map[string]model.Observation{}
	var newest int64
	for _, p := range points {
		id := engine.AnalysisInputIdentity(p)
		old, ok := latestIdentity[id]
		if !ok || old.Revision <= p.Revision {
			latestIdentity[id] = p
		}
		newest = max(newest, p.ObservedMS)
	}
	for _, p := range latestIdentity {
		key := p.DeviceID + "\x00" + p.Key
		old, ok := latestField[key]
		if !ok || shadowBefore(old, p) {
			latestField[key] = p
		}
	}
	result := []model.Observation{}
	for _, p := range latestIdentity {
		if window > 0 && p.ObservedMS > newest-window || latestField[p.DeviceID+"\x00"+p.Key].ID == p.ID {
			result = append(result, p)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return shadowBefore(result[i], result[j]) })
	return result
}

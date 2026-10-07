package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// PublishedPlan returns the persisted production plan, including its integrity checks.
func (s *Service) PublishedPlan(ctx context.Context, d model.Definition) (*model.ExecutionPlan, error) {
	return s.loadPlan(ctx, d)
}

func AnalysisMatches(d model.Definition, p model.Observation, assets []model.AssetVersion) bool {
	return simulationMatches(d.Selector, p, assets)
}

func NormalizeAnalysis(snapshot *historymodel.Snapshot) error {
	if len(snapshot.Points) < 1 || len(snapshot.Points) > historymodel.MaxPoints {
		return fmt.Errorf("analysis requires 1..%d inputs; reduce the requested interval", historymodel.MaxPoints)
	}
	if len(snapshot.History) > historymodel.MaxHistory || len(snapshot.AssetVersions) > historymodel.MaxHistory {
		return errors.New("analysis snapshot exceeds history or asset budget; reduce the interval")
	}
	if snapshot.FromMS <= 0 || snapshot.ToMS < snapshot.FromMS {
		return errors.New("analysis requires a positive from_ms and to_ms >= from_ms")
	}
	if snapshot.Order == "" {
		snapshot.Order = "event_time"
	}
	if snapshot.Order != "event_time" && snapshot.Order != "provided" {
		return errors.New("order must be event_time or provided")
	}
	clock := snapshot.Clock
	if clock.StepMS < 0 || clock.StepMS > math.MaxInt64/int64(len(snapshot.Points)) || clock.FreshnessMS != nil && *clock.FreshnessMS < 0 {
		return errors.New("invalid analysis logical clock")
	}
	for _, start := range []*int64{clock.AtMS, clock.FreshnessAtMS} {
		if start != nil && (*start <= 0 || *start > math.MaxInt64-clock.StepMS*int64(len(snapshot.Points)-1)) {
			return errors.New("analysis logical clock exceeds int64 range")
		}
	}
	ids := map[string]bool{}
	logicalIDs := map[string]bool{}
	for i, p := range snapshot.Points {
		if p.ID == "" || p.DeviceID == "" || p.Key == "" || p.ObservedMS < snapshot.FromMS || p.ObservedMS > snapshot.ToMS || !has([]string{"GOOD", "BAD", "UNCERTAIN"}, p.Quality) {
			return fmt.Errorf("invalid analysis input at index %d", i)
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate analysis input identity %s; supply one explicit revision per input", p.ID)
		}
		ids[p.ID] = true
		logicalIDs[historymodel.InputIdentity(p)] = true
	}
	seen := map[string]string{}
	history := []model.Observation{}
	for _, p := range snapshot.History {
		if p.ID == "" || p.DeviceID == "" || p.Key == "" || p.ObservedMS <= 0 || !has([]string{"GOOD", "BAD", "UNCERTAIN"}, p.Quality) {
			return errors.New("invalid analysis history input")
		}
		if ids[p.ID] || logicalIDs[historymodel.InputIdentity(p)] {
			continue
		}
		h := store.Hash(p)
		if old, ok := seen[p.ID]; ok {
			if old != h {
				return errors.New("conflicting historical observation identity")
			}
			continue
		}
		seen[p.ID] = h
		history = append(history, p)
	}
	snapshot.History = history
	sortReplay(snapshot.History)
	if snapshot.Order == "event_time" {
		sortReplay(snapshot.Points)
	}
	seenAssets := map[string]bool{}
	for _, a := range snapshot.AssetVersions {
		k := fmt.Sprintf("%s:%d", a.ID, a.Version)
		if a.ID == "" || a.Version < 1 || a.EffectiveMS < 0 || a.SamplingMS < 0 || a.SamplingMS > math.MaxInt64/3 || seenAssets[k] {
			return errors.New("invalid or duplicate analysis asset version")
		}
		seenAssets[k] = true
	}
	sort.Slice(snapshot.AssetVersions, func(i, j int) bool {
		a, b := snapshot.AssetVersions[i], snapshot.AssetVersions[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Version < b.Version
	})
	if snapshot.InitialState == nil {
		snapshot.InitialState = historymodel.State{}
		snapshot.InitialStateSource = "empty"
	} else if snapshot.InitialStateSource == "" {
		snapshot.InitialStateSource = "provided"
	}
	snapshot.HistorySHA256 = store.Hash(snapshot.History)
	snapshot.InputSHA256 = historymodel.InputDigest(*snapshot)
	return nil
}

// AnalysisStep executes only saved input and plan content. It never queries or
// mutates the formal engine state, alarm projection, outbox, or device controls.
func AnalysisStep(ctx context.Context, run historymodel.Run, snapshot historymodel.Snapshot, index int) (historymodel.Step, map[string]historymodel.State, error) {
	if index < 0 || index >= len(snapshot.Points) {
		return historymodel.Step{}, nil, errors.New("analysis cursor is outside snapshot")
	}
	p := snapshot.Points[index]
	clock, fresh := p.ObservedMS, p.ObservedMS
	if snapshot.Clock.AtMS != nil {
		clock = *snapshot.Clock.AtMS + int64(index)*snapshot.Clock.StepMS
	}
	fresh = clock
	if snapshot.Clock.FreshnessAtMS != nil {
		fresh = *snapshot.Clock.FreshnessAtMS + int64(index)*snapshot.Clock.StepMS
	}
	result := historymodel.Step{ID: fmt.Sprintf("%s:%d", run.ID, index), RunID: run.ID, InputIndex: index, Point: p, InputSHA256: store.Hash(p), ClockMS: clock, FreshnessAtMS: fresh, Lanes: []historymodel.Lane{}, FormalOutputs: []model.Observation{}}
	states := run.FinalState
	if states == nil {
		states = map[string]historymodel.State{}
	}
	chosen := []struct {
		side string
		rule historymodel.Rule
	}{}
	if run.Kind == "compare" {
		for _, side := range []struct {
			name    string
			version int64
		}{{"left", run.LeftVersion}, {"right", run.RightVersion}} {
			found := false
			for _, r := range snapshot.Rules {
				if r.Definition.Version == side.version {
					chosen = append(chosen, struct {
						side string
						rule historymodel.Rule
					}{side.name, r})
					found = true
					break
				}
			}
			if !found {
				return result, nil, errors.New("requested comparison version missing from saved snapshot")
			}
		}
	} else if run.Kind == "shadow" {
		if len(snapshot.Rules) == 0 {
			return result, nil, errors.New("candidate plan missing from saved snapshot")
		}
		chosen = append(chosen, struct {
			side string
			rule historymodel.Rule
		}{"candidate", snapshot.Rules[0]})
	} else {
		var found *historymodel.Rule
		for i := range snapshot.Rules {
			r := &snapshot.Rules[i]
			if r.Definition.EffectiveMS <= p.ObservedMS && (found == nil || r.Definition.Version > found.Definition.Version) {
				found = r
			}
		}
		if found == nil {
			return result, nil, fmt.Errorf("no historical definition version at %d", p.ObservedMS)
		}
		chosen = append(chosen, struct {
			side string
			rule historymodel.Rule
		}{fmt.Sprintf("version:%d", found.Definition.Version), *found})
	}
	for _, entry := range chosen {
		d, plan := entry.rule.Definition, entry.rule.Plan
		lane := historymodel.Lane{Side: entry.side, DefinitionVersion: d.Version, EffectiveMS: d.EffectiveMS, ActionIntents: []model.Step{}}
		if d.Status != "published" {
			lane.Skipped = "definition_inactive"
			result.Lanes = append(result.Lanes, lane)
			continue
		}
		if plan == nil {
			return result, nil, errors.New("saved production plan unavailable")
		}
		if err := rulecore.Verify(plan, d); err != nil {
			return result, nil, err
		}
		lane.PlanID, lane.PlanSHA256 = plan.ID, plan.SHA256
		if !simulationMatches(d.Selector, p, snapshot.AssetVersions) {
			lane.Skipped = "outside_selector"
			result.Lanes = append(result.Lanes, lane)
			continue
		}
		entity := p.DeviceID
		if d.Selector.AssetID != "" {
			entity = d.Selector.AssetID
		}
		if states[entry.side] == nil {
			states[entry.side] = historymodel.State{}
		}
		initial, ok := states[entry.side][entity]
		if !ok {
			initial = snapshot.InitialState[entity]
		}
		frame := rulecore.Frame{Observation: p, ClockMS: clock, FreshnessAtMS: fresh, FreshnessMS: 5000, EntityID: entity, InitialState: initial, Window: []model.Observation{}, InputFields: map[string]model.Observation{}, AssetVersions: []model.AssetVersion{}, Historical: true}
		for id, depth := p.DeviceID, 0; id != "" && depth < 64; depth++ {
			a, ok := simulationAssetAt(snapshot.AssetVersions, id, p.ObservedMS)
			if !ok {
				break
			}
			frame.AssetVersions = append(frame.AssetVersions, a)
			if id == p.DeviceID && a.SamplingMS > frame.FreshnessMS/3 {
				frame.FreshnessMS = a.SamplingMS * 3
			}
			id = a.ParentID
		}
		if d.Policy.FreshnessMS > 0 {
			frame.FreshnessMS = d.Policy.FreshnessMS
		}
		if snapshot.Clock.FreshnessMS != nil {
			frame.FreshnessMS = *snapshot.Clock.FreshnessMS
		}
		available := append(append([]model.Observation{}, snapshot.History...), snapshot.Points[:index+1]...)
		for i, item := range available {
			if i%256 == 0 {
				if err := ctx.Err(); err != nil {
					return result, nil, err
				}
			}
			if item.ObservedMS > p.ObservedMS {
				continue
			}
			if d.Selector.WindowMS > 0 && item.ObservedMS >= p.ObservedMS-d.Selector.WindowMS+1 && simulationMatches(d.Selector, item, snapshot.AssetVersions) {
				frame.Window = append(frame.Window, item)
			}
			if item.DeviceID == p.DeviceID {
				old, ok := frame.InputFields[item.Key]
				if !ok || observationLess(old, item) {
					frame.InputFields[item.Key] = item
				}
			}
		}
		if d.Selector.WindowMS <= 0 {
			frame.Window = []model.Observation{p}
		}
		sortReplay(frame.Window)
		execution, cancel := context.WithTimeout(ctx, time.Duration(max(int64(1), timeout(d).Milliseconds()))*time.Millisecond)
		evaluation, err := rulecore.Execute(execution, plan, frame)
		cancel()
		if err != nil {
			return result, nil, fmt.Errorf("input %s version %d: %w", p.ID, d.Version, err)
		}
		evaluation.RunID = run.ID
		lane.Evaluation = evaluation
		states[entry.side][entity] = evaluation.States
		if evaluation.Trigger {
			lane.ActionIntents = append(lane.ActionIntents, d.Policy.Steps...)
		}
		result.Lanes = append(result.Lanes, lane)
	}
	if len(result.Lanes) == 2 {
		result.Difference = AnalysisDifference(result.Lanes[0], result.Lanes[1])
	}
	if run.Kind == "shadow" {
		result.FormalInputID = AnalysisInputIdentity(p)
		result.FormalStatus = "no_corresponding_formal_evaluation"
	}
	return result, states, nil
}

func AnalysisInputIdentity(p model.Observation) string { return historymodel.InputIdentity(p) }
func AnalysisDifference(a, b historymodel.Lane) historymodel.Difference {
	return historymodel.Compare(a, b)
}

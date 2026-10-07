package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

const SimulationMaxPoints = 1000
const SimulationMaxHistory = 40000
const SimulationTimeoutMS = 5000

// A nil clock instant uses each observation's event time. Explicit instants
// advance by step_ms in execution order, including provided order.
type SimulationClock struct {
	AtMS          *int64 `json:"at_ms,omitempty"`
	StepMS        int64  `json:"step_ms,omitempty"`
	FreshnessAtMS *int64 `json:"freshness_at_ms,omitempty"`
	FreshnessMS   *int64 `json:"freshness_ms,omitempty"`
}

type SimulationRequest struct {
	ExpectedVersion *int64                             `json:"expected_version,omitempty"`
	Point           *model.Observation                 `json:"point,omitempty"`
	Points          []model.Observation                `json:"points,omitempty"`
	History         []model.Observation                `json:"history,omitempty"`
	InitialState    map[string]map[string]RuntimeState `json:"initial_state,omitempty"`
	Clock           SimulationClock                    `json:"clock,omitempty"`
	Order           string                             `json:"order,omitempty"`
	AssetVersions   []model.AssetVersion               `json:"asset_versions,omitempty"`
	TimeoutMS       int64                              `json:"timeout_ms,omitempty"`
}

// Preserve an explicitly empty snapshot when Go clients serialize a request.
// Nil means omission/database snapshot; [] means a deliberate empty snapshot.
func (request SimulationRequest) MarshalJSON() ([]byte, error) {
	type wire SimulationRequest
	data, err := json.Marshal(wire(request))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, item := range map[string]struct {
		present bool
		value   any
	}{
		"history":        {request.History != nil, request.History},
		"points":         {request.Points != nil, request.Points},
		"asset_versions": {request.AssetVersions != nil, request.AssetVersions},
	} {
		if item.present {
			fields[key], err = json.Marshal(item.value)
			if err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(fields)
}

type SimulationStep struct {
	InputIndex    int               `json:"input_index"`
	Point         model.Observation `json:"point"`
	ClockMS       int64             `json:"clock_ms"`
	FreshnessAtMS int64             `json:"freshness_at_ms"`
	FreshnessMS   int64             `json:"freshness_ms"`
	Evaluation    Evaluation        `json:"evaluation"`
}

type SimulationResult struct {
	DraftRevision int64                              `json:"draft_revision"`
	PlanID        string                             `json:"plan_id"`
	PlanSHA256    string                             `json:"plan_sha256"`
	ContentSHA256 string                             `json:"content_sha256"`
	Order         string                             `json:"order"`
	Clock         SimulationClock                    `json:"clock"`
	Results       []SimulationStep                   `json:"results"`
	FinalState    map[string]map[string]RuntimeState `json:"final_state"`
	InputSHA256   string                             `json:"input_sha256"`
	HistorySHA256 string                             `json:"history_sha256"`
	HistorySource string                             `json:"history_source"`
	History       []model.Observation                `json:"history"`
	AssetVersions []model.AssetVersion               `json:"asset_versions"`
	TimeoutMS     int64                              `json:"timeout_ms"`
}

func (s *Service) SimulateSequence(ctx context.Context, definition model.Definition, request SimulationRequest) (SimulationResult, error) {
	return s.SimulateSequenceAuthorized(ctx, definition, request, nil)
}

// SimulateSequenceAuthorized snapshots all external data before execution. The
// authorization callback checks supplied and loaded references before they can
// affect a result. Trusted in-process callers can use SimulateSequence.
func (s *Service) SimulateSequenceAuthorized(ctx context.Context, definition model.Definition, request SimulationRequest, authorize func(context.Context, string) error) (SimulationResult, error) {
	result := SimulationResult{Results: []SimulationStep{}, FinalState: map[string]map[string]RuntimeState{}, History: []model.Observation{}, AssetVersions: []model.AssetVersion{}}
	limit := request.TimeoutMS
	if limit == 0 {
		limit = SimulationTimeoutMS
	}
	if limit < 1 || limit > SimulationTimeoutMS {
		return result, fmt.Errorf("timeout_ms must be 1..%d", SimulationTimeoutMS)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(limit)*time.Millisecond)
	defer cancel()
	result.TimeoutMS = limit
	if err := ctx.Err(); err != nil {
		return result, err
	}
	points := append([]model.Observation{}, request.Points...)
	if request.Point != nil {
		if request.Points != nil {
			return result, errors.New("provide point or points, not both")
		}
		points = []model.Observation{*request.Point}
	}
	if len(points) < 1 || len(points) > SimulationMaxPoints {
		return result, fmt.Errorf("simulation requires 1..%d points", SimulationMaxPoints)
	}
	if len(request.History) > SimulationMaxHistory {
		return result, fmt.Errorf("simulation history exceeds %d points", SimulationMaxHistory)
	}
	if len(request.AssetVersions) > SimulationMaxHistory {
		return result, errors.New("simulation asset snapshot exceeds budget")
	}
	order := request.Order
	if order == "" {
		order = "event_time"
	}
	if order != "event_time" && order != "provided" {
		return result, errors.New("order must be event_time or provided")
	}
	result.Order, result.Clock = order, request.Clock
	if request.Clock.StepMS < 0 || request.Clock.FreshnessMS != nil && *request.Clock.FreshnessMS < 0 {
		return result, errors.New("clock step and freshness must be nonnegative")
	}
	if request.Clock.StepMS > math.MaxInt64/int64(len(points)) {
		return result, errors.New("clock step exceeds int64 range")
	}
	for _, start := range []*int64{request.Clock.AtMS, request.Clock.FreshnessAtMS} {
		if start != nil && (*start <= 0 || *start > math.MaxInt64-request.Clock.StepMS*int64(len(points)-1)) {
			return result, errors.New("clock instant exceeds permitted range")
		}
	}
	check := func(point model.Observation) error {
		if point.DeviceID == "" || point.Key == "" || point.ObservedMS <= 0 || !has([]string{"GOOD", "BAD", "UNCERTAIN"}, point.Quality) {
			return errors.New("simulation sample requires device, key, positive timestamp and valid quality")
		}
		if authorize != nil {
			return authorize(ctx, point.DeviceID)
		}
		return ctx.Err()
	}
	indices, inputIDs := map[string]int{}, map[string]bool{}
	for i := range points {
		if err := check(points[i]); err != nil {
			return result, err
		}
		if points[i].ID == "" {
			points[i].ID = fmt.Sprintf("simulation:%d", i)
		}
		if inputIDs[points[i].ID] {
			return result, fmt.Errorf("duplicate simulation input id %s", points[i].ID)
		}
		inputIDs[points[i].ID], indices[points[i].ID] = true, i
	}
	if order == "event_time" {
		sortReplay(points)
	}
	plan, err := s.prepareDraft(ctx, definition)
	if err != nil {
		return result, err
	}
	result.PlanID, result.PlanSHA256, result.ContentSHA256 = plan.ID, plan.SHA256, plan.ContentSHA256
	history := append([]model.Observation{}, request.History...)
	result.HistorySource = "provided"
	if request.History == nil {
		result.HistorySource = "database_snapshot"
		history, err = s.simulationHistory(ctx, definition, plan, points)
		if err != nil {
			return result, err
		}
	}
	for i := range history {
		if err := check(history[i]); err != nil {
			return result, err
		}
		if history[i].ID == "" {
			history[i].ID = fmt.Sprintf("history:%d", i)
		}
	}
	// The supplied sequence replaces matching stored observations as the old
	// point endpoint did. Future sequence inputs enter only when processed.
	filtered, seen := []model.Observation{}, map[string]string{}
	for _, point := range history {
		if inputIDs[point.ID] {
			continue
		}
		hash := store.Hash(point)
		if old, exists := seen[point.ID]; exists {
			if old != hash {
				return result, fmt.Errorf("conflicting history input id %s", point.ID)
			}
			continue
		}
		seen[point.ID] = hash
		filtered = append(filtered, point)
	}
	history = filtered
	sortReplay(history)
	assets, err := s.simulationAssets(ctx, append(append([]model.Observation{}, history...), points...), request.AssetVersions)
	if err != nil {
		return result, err
	}
	for _, asset := range assets {
		if authorize != nil {
			if err := authorize(ctx, asset.ID); err != nil {
				return result, err
			}
			if asset.ParentID != "" {
				if err := authorize(ctx, asset.ParentID); err != nil {
					return result, err
				}
			}
		}
	}
	result.AssetVersions, result.History = assets, history
	result.HistorySHA256 = store.Hash(history)
	for entity := range request.InitialState {
		if authorize != nil {
			if err := authorize(ctx, entity); err != nil {
				return result, err
			}
		}
	}
	// Hash the exact normalized external inputs, including the database snapshot
	// and asset versions, rather than relying on a mutable database query alone.
	hasher := sha256.New()
	encoder := json.NewEncoder(hasher)
	for _, value := range []any{plan.ID, order, request.Clock, points, history, request.InitialState, assets} {
		if err := encoder.Encode(value); err != nil {
			return result, err
		}
	}
	result.InputSHA256 = hex.EncodeToString(hasher.Sum(nil))
	available := append([]model.Observation{}, history...)
	for i, point := range points {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !simulationMatches(definition.Selector, point, assets) {
			return result, fmt.Errorf("simulation point %s is outside the definition selector", point.ID)
		}
		entity := point.DeviceID
		if definition.Selector.AssetID != "" {
			entity = definition.Selector.AssetID
		}
		states, exists := result.FinalState[entity]
		if !exists {
			states = request.InitialState[entity]
		}
		clock := point.ObservedMS
		if request.Clock.AtMS != nil {
			clock = *request.Clock.AtMS + int64(i)*request.Clock.StepMS
		}
		freshnessAt := clock
		if request.Clock.FreshnessAtMS != nil {
			freshnessAt = *request.Clock.FreshnessAtMS + int64(i)*request.Clock.StepMS
		}
		frame := rulecore.Frame{Observation: point, EntityID: entity, ClockMS: clock, FreshnessAtMS: freshnessAt, FreshnessMS: 5000, InitialState: states, Window: []model.Observation{}, InputFields: map[string]model.Observation{}, AssetVersions: []model.AssetVersion{}, Historical: true}
		for id, depth := point.DeviceID, 0; id != "" && depth < 64; depth++ {
			asset, found := simulationAssetAt(assets, id, point.ObservedMS)
			if !found {
				break
			}
			frame.AssetVersions = append(frame.AssetVersions, asset)
			if id == point.DeviceID && asset.SamplingMS > frame.FreshnessMS/3 && asset.SamplingMS <= math.MaxInt64/3 {
				frame.FreshnessMS = asset.SamplingMS * 3
			}
			id = asset.ParentID
		}
		if definition.Policy.FreshnessMS > 0 {
			frame.FreshnessMS = definition.Policy.FreshnessMS
		}
		if request.Clock.FreshnessMS != nil {
			frame.FreshnessMS = *request.Clock.FreshnessMS
		}
		available = append(available, point)
		for j, item := range available {
			if j%256 == 0 {
				if err := ctx.Err(); err != nil {
					return result, err
				}
			}
			if item.ObservedMS > point.ObservedMS {
				continue
			}
			if definition.Selector.WindowMS > 0 && item.ObservedMS >= point.ObservedMS-definition.Selector.WindowMS+1 && simulationMatches(definition.Selector, item, assets) {
				frame.Window = append(frame.Window, item)
			}
			if item.DeviceID == point.DeviceID {
				previous, found := frame.InputFields[item.Key]
				if !found || observationLess(previous, item) {
					frame.InputFields[item.Key] = item
				}
			}
		}
		if definition.Selector.WindowMS <= 0 {
			frame.Window = []model.Observation{point}
		}
		if len(frame.Window) > SimulationMaxHistory {
			return result, errors.New("simulation window exceeds query budget")
		}
		sortReplay(frame.Window)
		execution, cancel := context.WithTimeout(ctx, timeout(definition))
		evaluation, err := rulecore.Execute(execution, plan, frame)
		cancel()
		if err != nil {
			return result, fmt.Errorf("simulation input %s: %w", point.ID, err)
		}
		result.FinalState[entity] = evaluation.States
		result.Results = append(result.Results, SimulationStep{InputIndex: indices[point.ID], Point: point, ClockMS: clock, FreshnessAtMS: freshnessAt, FreshnessMS: frame.FreshnessMS, Evaluation: evaluation})
	}
	return result, nil
}

func observationLess(a, b model.Observation) bool {
	if a.ObservedMS != b.ObservedMS {
		return a.ObservedMS < b.ObservedMS
	}
	if a.SourceSequence != b.SourceSequence {
		return a.SourceSequence < b.SourceSequence
	}
	return a.ID < b.ID
}

func (s *Service) simulationHistory(ctx context.Context, definition model.Definition, plan *model.ExecutionPlan, points []model.Observation) ([]model.Observation, error) {
	from, to := points[0].ObservedMS, points[0].ObservedMS
	ids := map[string]bool{}
	for _, point := range points {
		from = min(from, point.ObservedMS)
		to = max(to, point.ObservedMS)
		ids[point.DeviceID] = true
	}
	devices := append([]string{}, definition.Selector.DeviceIDs...)
	if len(devices) == 0 && definition.Selector.AssetID == "" {
		for id := range ids {
			devices = append(devices, id)
		}
		sort.Strings(devices)
	}
	keys := append([]string{}, definition.Selector.Keys...)
	extraKeys := map[string]bool{}
	for _, node := range plan.Nodes {
		if node.Type == "input" && node.Params.Key != "" {
			extraKeys[node.Params.Key] = true
			if !has(keys, node.Params.Key) && len(definition.Selector.Keys) > 0 {
				keys = append(keys, node.Params.Key)
			}
		}
	}
	history := []model.Observation{}
	if definition.Selector.WindowMS > 0 || len(extraKeys) > 0 {
		queryFrom := max(int64(0), from-max(int64(1), definition.Selector.WindowMS)+1)
		data, err := s.Store.Query(ctx, store.Query{DeviceIDs: devices, Keys: keys, FromMS: queryFrom, ToMS: to, Limit: SimulationMaxHistory})
		if err != nil {
			return nil, err
		}
		if data.Truncated {
			return nil, errors.New("simulation history exceeds query budget")
		}
		history = append(history, data.Points...)
		for device := range ids {
			for key := range extraKeys {
				data, err := s.Store.Query(ctx, store.Query{DeviceIDs: []string{device}, Keys: []string{key}, FromMS: 0, ToMS: queryFrom, Limit: 1})
				if err != nil {
					return nil, err
				}
				history = append(history, data.Points...)
			}
		}
	}
	if len(history) > SimulationMaxHistory {
		return nil, errors.New("simulation history exceeds query budget")
	}
	return history, nil
}

func (s *Service) simulationAssets(ctx context.Context, points []model.Observation, supplied []model.AssetVersion) ([]model.AssetVersion, error) {
	assets := append([]model.AssetVersion{}, supplied...)
	if supplied == nil {
		pending, visited := []string{}, map[string]bool{}
		for _, point := range points {
			pending = append(pending, point.DeviceID)
		}
		for len(pending) > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			id := pending[0]
			pending = pending[1:]
			if id == "" || visited[id] {
				continue
			}
			visited[id] = true
			if len(visited) > SimulationMaxHistory {
				return nil, errors.New("simulation asset hierarchy exceeds budget")
			}
			documents, err := s.Store.Versions(ctx, "entity", id)
			if err != nil {
				return nil, err
			}
			for _, document := range documents {
				entity, err := store.Decode[model.Entity](document)
				if err != nil {
					return nil, err
				}
				assets = append(assets, model.AssetVersion{ID: id, Version: document.Version, EffectiveMS: document.UpdatedMS, ParentID: entity.ParentID, SamplingMS: entity.SamplingMS})
				pending = append(pending, entity.ParentID)
			}
		}
	}
	if len(assets) > SimulationMaxHistory {
		return nil, errors.New("simulation asset snapshot exceeds budget")
	}
	seen := map[string]bool{}
	for _, asset := range assets {
		if asset.ID == "" || asset.Version < 1 || asset.EffectiveMS < 0 || asset.SamplingMS < 0 || asset.SamplingMS > math.MaxInt64/3 {
			return nil, errors.New("invalid simulation asset version")
		}
		key := fmt.Sprintf("%s:%d", asset.ID, asset.Version)
		if seen[key] {
			return nil, errors.New("duplicate simulation asset version")
		}
		seen[key] = true
	}
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].ID != assets[j].ID {
			return assets[i].ID < assets[j].ID
		}
		return assets[i].Version < assets[j].Version
	})
	return assets, nil
}

func simulationAssetAt(assets []model.AssetVersion, id string, at int64) (model.AssetVersion, bool) {
	var found model.AssetVersion
	for _, asset := range assets {
		if asset.ID == id && asset.EffectiveMS <= at && asset.Version > found.Version {
			found = asset
		}
	}
	return found, found.ID != ""
}

func simulationMatches(selector model.Selector, point model.Observation, assets []model.AssetVersion) bool {
	if len(selector.Keys) > 0 && !has(selector.Keys, point.Key) || len(selector.DeviceIDs) > 0 && !has(selector.DeviceIDs, point.DeviceID) {
		return false
	}
	if selector.AssetID == "" {
		return true
	}
	for id, depth := point.DeviceID, 0; id != "" && depth < 64; depth++ {
		if id == selector.AssetID {
			return true
		}
		asset, found := simulationAssetAt(assets, id, point.ObservedMS)
		if !found {
			break
		}
		id = asset.ParentID
	}
	return false
}

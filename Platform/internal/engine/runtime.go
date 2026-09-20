package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type RuntimeState struct {
	Active    bool  `json:"active"`
	Candidate bool  `json:"candidate"`
	SinceMS   int64 `json:"since_ms"`
	Count     int64 `json:"count"`
	Previous  any   `json:"previous"`
	LastMS    int64 `json:"last_ms"`
}
type Evaluation struct {
	DefinitionID string                  `json:"definition_id"`
	Version      int64                   `json:"version"`
	EntityID     string                  `json:"entity_id"`
	AtMS         int64                   `json:"at_ms"`
	Values       map[string]any          `json:"values"`
	States       map[string]RuntimeState `json:"states"`
	Trigger      bool                    `json:"trigger"`
	Alarm        *bool                   `json:"alarm,omitempty"`
	Severity     string                  `json:"severity"`
	Quality      model.QualitySummary    `json:"quality"`
	Historical   bool                    `json:"historical"`
	RunID        string                  `json:"run_id,omitempty"`
}

type simulationSampleKey struct{}
type evaluationKindKey struct{}

func (s *Service) Simulate(ctx context.Context, d model.Definition, p model.Observation) (Evaluation, error) {
	if p.DeviceID == "" || p.Key == "" || p.ObservedMS <= 0 || !has([]string{"GOOD", "BAD", "UNCERTAIN"}, p.Quality) {
		return Evaluation{}, errors.New("simulation sample requires device, key, positive timestamp and valid quality")
	}
	matched, err := s.scope(ctx, d, p)
	if err != nil {
		return Evaluation{}, err
	}
	if !matched {
		return Evaluation{}, errors.New("simulation sample is outside the definition selector")
	}
	return s.Evaluate(context.WithValue(ctx, simulationSampleKey{}, true), d, p, true, nil)
}

func (s *Service) scope(ctx context.Context, d model.Definition, p model.Observation) (bool, error) {
	if len(d.Selector.Keys) > 0 && !has(d.Selector.Keys, p.Key) {
		return false, nil
	}
	if len(d.Selector.DeviceIDs) > 0 && !has(d.Selector.DeviceIDs, p.DeviceID) {
		return false, nil
	}
	if d.Selector.AssetID != "" {
		id := p.DeviceID
		for i := 0; i < 64 && id != ""; i++ {
			if id == d.Selector.AssetID {
				return true, nil
			}
			doc, e := s.Store.VersionAt(ctx, "entity", id, p.ObservedMS)
			if errors.Is(e, store.ErrNotFound) {
				return false, nil
			}
			if e != nil {
				return false, e
			}
			entity, e := store.Decode[model.Entity](doc)
			if e != nil {
				return false, e
			}
			id = entity.ParentID
		}
		return false, nil
	}
	return true, nil
}
func (s *Service) window(ctx context.Context, d model.Definition, p model.Observation) ([]model.Observation, error) {
	if d.Selector.WindowMS <= 0 {
		return []model.Observation{p}, nil
	}
	ids := d.Selector.DeviceIDs
	if len(ids) == 0 && d.Selector.AssetID == "" {
		ids = []string{p.DeviceID}
	}
	r, e := s.Store.Query(ctx, store.Query{DeviceIDs: ids, Keys: d.Selector.Keys, FromMS: p.ObservedMS - d.Selector.WindowMS + 1, ToMS: p.ObservedMS, Limit: 40000})
	if e != nil {
		return nil, e
	}
	if r.Truncated {
		return nil, errors.New("analysis window exceeds query budget; reduce the window or use rollup input")
	}
	out := []model.Observation{}
	simulation, _ := ctx.Value(simulationSampleKey{}).(bool)
	for _, item := range r.Points {
		if simulation && item.ID == p.ID {
			continue
		}
		ok, e := s.scope(ctx, d, item)
		if e != nil {
			return nil, e
		}
		if ok {
			out = append(out, item)
		}
	}
	if simulation {
		out = append(out, p)
	}
	return out, nil
}
func (s *Service) Evaluate(ctx context.Context, d model.Definition, p model.Observation, historical bool, states map[string]RuntimeState) (Evaluation, error) {
	result := Evaluation{DefinitionID: d.ID, Version: d.Version, EntityID: p.DeviceID, AtMS: p.ObservedMS, Values: map[string]any{}, States: map[string]RuntimeState{}, Historical: historical, Severity: "WARNING", Quality: model.QualitySummary{Completeness: "unknown"}}
	clockMS := p.ObservedMS
	if at, ok := ctx.Value(timerAtKey{}).(int64); ok {
		clockMS, result.AtMS = at, at
	}
	freshness, err := s.freshness(ctx, d, p.DeviceID)
	if err != nil {
		return result, err
	}
	if d.Selector.AssetID != "" {
		result.EntityID = d.Selector.AssetID
	}
	if states == nil {
		states = map[string]RuntimeState{}
	}
	for id, state := range states {
		result.States[id] = state
	}
	v := s.Validate(ctx, d)
	if !v.Valid {
		return result, fmt.Errorf("invalid definition: %v", v.Errors)
	}
	points, e := s.window(ctx, d, p)
	if e != nil {
		return result, e
	}
	agg := store.AggregatePoints(points)
	for _, point := range points {
		if point.Quality == "GOOD" {
			result.Quality.Good++
		} else if point.Quality == "BAD" {
			result.Quality.Bad++
			result.Quality.Excluded++
		} else if point.Quality == "UNCERTAIN" {
			result.Quality.Uncertain++
			result.Quality.Excluded++
		}
	}
	nodes := map[string]model.Node{}
	for _, n := range d.Nodes {
		nodes[n.ID] = n
	}
	for _, id := range v.Order {
		if e = ctx.Err(); e != nil {
			return result, e
		}
		n := nodes[id]
		var value any = p.Value
		enabled := true
		for _, c := range d.Connections {
			if c.To == id {
				outputKey := c.From
				if c.FromPort == "error" {
					outputKey += ".error"
				}
				up, exists := result.Values[outputKey]
				if !exists {
					enabled = false
					break
				}
				if c.FromPort == "true" && !truth(up) {
					enabled = false
					break
				}
				if c.FromPort == "false" && truth(up) {
					enabled = false
					break
				}
				value = up
			}
		}
		if !enabled {
			continue
		}
		state := states[id]
		before := state
		vars := map[string]any{"value": value, "sum": agg.Sum, "count": agg.Count, "min": agg.Min, "max": agg.Max, "avg": nil, "previous": state.Previous, "active": state.Active, "good": p.Quality == "GOOD", "quality": p.Quality, "fresh": historical || s.Store.Now().UnixMilli()-p.ObservedMS <= freshness}
		if agg.Average != nil {
			vars["avg"] = *agg.Average
		}
		if vars["previous"] == nil {
			vars["previous"] = 0
		}
		switch n.Type {
		case "input":
			if key, ok := n.Params["key"].(string); ok && key != "" {
				if key != p.Key {
					obs, e := s.Store.Query(ctx, store.Query{DeviceIDs: []string{p.DeviceID}, Keys: []string{key}, FromMS: 0, ToMS: p.ObservedMS, Limit: 1})
					if e != nil {
						return result, e
					}
					if len(obs.Points) == 0 {
						return result, fmt.Errorf("input %s is missing", key)
					}
					value = obs.Points[0].Value
				}
			}
		case "aggregate":
			fn, _ := n.Params["function"].(string)
			if agg.Count == 0 {
				value = nil
			} else {
				switch fn {
				case "sum":
					value = agg.Sum
				case "count":
					value = agg.Count
				case "min":
					value = agg.Min
				case "max":
					value = agg.Max
				case "avg":
					value = *agg.Average
				}
			}
		case "expression":
			if d.Kind == "analysis" && result.Quality.Good == 0 {
				value = nil
				break
			}
			code, _ := n.Params["code"].(string)
			scriptContext, cancel := context.WithTimeout(ctx, timeout(d))
			value, e = Expression(scriptContext, code, vars)
			cancel()
			if e != nil {
				if !hasErrorBranch(d, id) {
					return result, fmt.Errorf("expression %s: %w", id, e)
				}
				result.Values[id+".error"] = e.Error()
				continue
			}
		case "threshold", "condition", "branch":
			operator, _ := n.Params["operator"].(string)
			if operator == "" {
				operator = ">="
			}
			value, e = compare(value, n.Params["value"], operator)
			if e != nil {
				return result, e
			}
			if p.Quality != "GOOD" {
				value = false
			}
		case "hysteresis":
			if p.Quality != "GOOD" {
				value = state.Active
				break
			}
			hi, lo := n.Params["high"], n.Params["low"]
			high, e := compare(value, hi, ">=")
			if e != nil {
				return result, e
			}
			low, e := compare(value, lo, "<=")
			if e != nil {
				return result, e
			}
			below, _ := n.Params["direction"].(string)
			if below == "below" && low {
				state.Active = true
			} else if below == "below" && high {
				state.Active = false
			} else if below == "below" {
			} else if high {
				state.Active = true
			} else if low {
				state.Active = false
			}
			value = state.Active
		case "debounce":
			candidate := truth(value)
			mode, _ := n.Params["mode"].(string)
			if mode == "activation" && !candidate {
				state.Candidate, state.Active, state.SinceMS = false, false, 0
				value = false
				break
			}
			if state.SinceMS == 0 || candidate != state.Candidate {
				state.Candidate = candidate
				state.SinceMS = clockMS
			}
			duration := integer(n.Params["duration_ms"], 1000)
			if clockMS-state.SinceMS >= duration {
				state.Active = candidate
			}
			value = state.Active
		case "counter":
			if p.Quality == "GOOD" {
				mode, _ := n.Params["mode"].(string)
				increment := int64(1)
				if mode == "delta" {
					increment = integer(value, 0)
				}
				if mode != "rising" || truth(value) && !truth(state.Previous) {
					state.Count += increment
				}
				state.Previous = value
			}
			value = state.Count
		case "alarm":
			if p.Quality != "GOOD" {
				continue
			}
			active := truth(value)
			result.Alarm = &active
			severity, _ := n.Params["severity"].(string)
			if severity != "" {
				result.Severity = severity
			}
		case "action":
			result.Trigger = result.Trigger || truth(value) && !state.Active
			state.Active = truth(value)
		case "output":
		}
		state.LastMS = clockMS
		if n.Type != "counter" {
			state.Previous = value
		}
		result.States[id] = state
		result.Values[id] = value
		_ = before
	}
	return result, nil
}
func hasErrorBranch(d model.Definition, id string) bool {
	for _, c := range d.Connections {
		if c.From == id && c.FromPort == "error" {
			return true
		}
	}
	return false
}
func compare(a, b any, op string) (bool, error) {
	valid := map[string]bool{"==": true, "!=": true, ">": true, ">=": true, "<": true, "<=": true}
	if !valid[op] {
		return false, errors.New("invalid comparison operator")
	}
	value, e := Expression(context.Background(), "value "+op+" threshold", map[string]any{"value": a, "threshold": b})
	return truth(value), e
}
func (s *Service) Process(ctx context.Context, p model.Observation, historical bool, runID string) error {
	if historical && runID == "" {
		if p.Late {
			return nil
		}
		return s.deferEvaluation(ctx, p)
	}
	depth, _ := ctx.Value(replayDepthKey{}).(int)
	if depth >= 64 {
		return errors.New("derived dependency depth exceeds 64")
	}
	if kind, _ := ctx.Value(evaluationKindKey{}).(string); !historical && kind != "calculation" {
		// Strategy state is independent from analytical replay. A historical
		// merge must not delay a fresh control decision beyond its freshness.
		if err := s.ProcessStrategies(ctx, p); err != nil {
			return err
		}
		ctx = context.WithValue(ctx, evaluationKindKey{}, "calculation")
	}
	s.mu.Lock()
	derived, err := s.processLocked(ctx, p, historical, runID)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if historical {
		ctx = context.WithValue(ctx, replayDepthKey{}, depth+1)
		for _, output := range derived {
			if err = s.Process(ctx, output, true, runID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) ProcessStrategies(ctx context.Context, p model.Observation) error {
	if p.Late || s.Store.Now().UnixMilli()-p.ObservedMS > 15000 {
		return nil
	}
	s.strategyMu.Lock()
	defer s.strategyMu.Unlock()
	_, err := s.processLocked(context.WithValue(ctx, evaluationKindKey{}, "strategy"), p, false, "")
	return err
}

func (s *Service) ProcessCalculations(ctx context.Context, p model.Observation, historical bool) error {
	return s.Process(context.WithValue(ctx, evaluationKindKey{}, "calculation"), p, historical, "")
}

type replayDepthKey struct{}
type replayStartKey struct{}

func (s *Service) Matches(ctx context.Context, d model.Definition, p model.Observation) (bool, error) {
	return s.scope(ctx, d, p)
}
func (s *Service) ProcessDefinition(ctx context.Context, id string, p model.Observation, historical bool) error {
	return s.Process(context.WithValue(ctx, replayFilterKey{}, map[string]bool{id: true}), p, historical, "")
}

func (s *Service) processLocked(ctx context.Context, p model.Observation, historical bool, runID string) ([]model.Observation, error) {
	derived := []model.Observation{}
	docs, e := s.Store.List(ctx, "definition")
	if e != nil {
		return nil, e
	}
	for _, doc := range docs {
		d, e := store.Decode[model.Definition](doc)
		if e != nil {
			return nil, e
		}
		if filter, ok := ctx.Value(replayFilterKey{}).(map[string]bool); ok && !filter[d.ID] {
			continue
		}
		if kind, _ := ctx.Value(evaluationKindKey{}).(string); kind == "strategy" && d.Kind != "strategy" || kind == "calculation" && d.Kind == "strategy" {
			continue
		}
		if historical && d.Kind == "strategy" {
			continue
		}
		if historical {
			d, e = s.Published(ctx, d.ID, p.ObservedMS)
			if errors.Is(e, store.ErrNotFound) {
				continue
			}
			if e != nil {
				return nil, e
			}
		}
		if d.Status != "published" {
			continue
		}
		match, e := s.scope(ctx, d, p)
		if e != nil {
			return nil, e
		}
		if !match {
			continue
		}
		if p.DefinitionID == d.ID {
			continue
		}
		inputID := p.OriginID
		if inputID == "" {
			inputID = p.ID
		}
		key := "evaluation:" + d.ID + ":" + strconv.FormatInt(d.Version, 10) + ":" + inputID
		if historical {
			key = runID + ":" + key
		} else {
			var savedHash string
			err := s.Store.DB.QueryRowContext(ctx, "SELECT payload_hash FROM inbox WHERE id=$1", key).Scan(&savedHash)
			if err == nil {
				if savedHash != store.Hash(p) {
					return nil, store.ErrConflict
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		entity := p.DeviceID
		if d.Selector.AssetID != "" {
			entity = d.Selector.AssetID
		}
		liveStateID := fmt.Sprintf("%s:%d:%s", d.ID, d.Version, entity)
		stateID := liveStateID
		states := map[string]RuntimeState{}
		kind := "engine_state"
		if historical {
			kind = "recompute_state"
			stateID = runID + ":" + stateID
		}
		if saved, e := s.Store.Get(ctx, kind, stateID); e == nil {
			if e = store.DecodeJSON(saved.Data, &states); e != nil {
				return nil, e
			}
		} else if !errors.Is(e, store.ErrNotFound) {
			return nil, e
		} else if historical {
			if start, ok := ctx.Value(replayStartKey{}).(int64); ok {
				var raw string
				err := s.Store.DB.QueryRowContext(ctx, "SELECT data FROM engine_checkpoints WHERE state_id=$1 AND at_ms<$2 ORDER BY at_ms DESC LIMIT 1", liveStateID, start).Scan(&raw)
				if err == nil {
					if err = store.DecodeJSON([]byte(raw), &states); err != nil {
						return nil, err
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
			}
		}
		if historical {
			ctx = context.WithValue(ctx, historicalAtKey{}, p.ObservedMS)
		}
		result, e := s.Evaluate(ctx, d, p, historical, states)
		if e != nil {
			return nil, e
		}
		result.RunID = runID
		outputs, err := s.commitEvaluation(ctx, key, kind, stateID, d, p, result)
		if err != nil {
			return nil, err
		}
		derived = append(derived, outputs...)
	}
	return derived, nil
}
func (s *Service) commitEvaluation(ctx context.Context, key, stateKind, stateID string, d model.Definition, input model.Observation, r Evaluation) ([]model.Observation, error) {
	outputs := []model.Observation{}
	// Read the previous projection before opening the write transaction.
	old := map[string]model.Observation{}
	for _, o := range d.Outputs {
		q, e := s.Store.Query(ctx, store.Query{DeviceIDs: []string{r.EntityID}, Keys: []string{d.ID + "." + o.Key}, FromMS: r.AtMS, ToMS: r.AtMS, Limit: 1})
		if e != nil {
			return nil, e
		}
		if len(q.Points) > 0 {
			old[o.Key] = q.Points[0]
		}
	}
	err := s.Store.Write(ctx, func(t *store.Tx) error {
		expires := s.Store.Now().AddDate(0, 0, max(s.Store.Policy().Retention.RawDays, int(s.Store.Policy().AutoBackfillDays))).UnixMilli()
		duplicate, e := t.InboxUntil(key, store.Hash(input), s.Store.NodeID, expires)
		if e != nil {
			return e
		}
		if duplicate {
			return nil
		}
		if e = t.SetEphemeral(stateKind, stateID, r.States); e != nil {
			return e
		}
		if r.Historical {
			info := replayStateInfo{LiveID: fmt.Sprintf("%s:%d:%s", d.ID, d.Version, r.EntityID), DefinitionID: d.ID, Version: d.Version, EntityID: r.EntityID, AtMS: r.AtMS}
			if e = t.SetEphemeral("recompute_state_info", stateID, info); e != nil {
				return e
			}
		}
		if !r.Historical {
			raw, e := json.Marshal(r.States)
			if e != nil {
				return e
			}
			if _, e = t.ExecContext(ctx, "INSERT INTO engine_checkpoints(state_id,bucket_ms,at_ms,data) VALUES($1,$2,$3,$4) ON CONFLICT(state_id,bucket_ms) DO UPDATE SET at_ms=excluded.at_ms,data=excluded.data WHERE excluded.at_ms>=engine_checkpoints.at_ms", stateID, r.AtMS/60000*60000, r.AtMS, string(raw)); e != nil {
				return e
			}
		}
		for _, o := range d.Outputs {
			value, ok := r.Values[o.NodeID]
			if !ok {
				continue
			}
			previous, existed := old[o.Key]
			quality := "GOOD"
			reason := ""
			if value == nil || r.Quality.Good == 0 {
				quality = "UNCERTAIN"
				reason = "no valid input in analysis window"
			}
			if existed && store.Hash(previous.Value) == store.Hash(value) && previous.Quality == quality {
				outputs = append(outputs, previous)
				continue
			}
			revision := previous.Revision + 1
			p := model.Observation{ID: store.Hash([]any{key, o.Key, value, quality}), MessageID: key, SourceID: s.Store.NodeID, DeviceID: r.EntityID, Key: d.ID + "." + o.Key, Value: value, ObservedMS: r.AtMS, ReceivedMS: s.Store.Now().UnixMilli(), Quality: quality, QualityReason: reason, TimeSource: "analysis", Unit: o.Unit, EntityRevision: input.EntityRevision, AssetVersion: input.AssetVersion, RuleVersion: d.Version, DefinitionID: d.ID, Late: r.Historical, Revision: revision}
			if e = t.InsertPoint(p); e != nil {
				return e
			}
			outputs = append(outputs, p)
			if e = t.Enqueue("tb:"+p.ID, "tb_telemetry", p.DeviceID, p); e != nil {
				return e
			}
			if e = t.Enqueue("engine:"+p.ID, "engine", p.DeviceID, p); e != nil {
				return e
			}
			if !p.Late {
				if e = t.Enqueue("strategy:"+p.ID, "strategy", p.DeviceID, p); e != nil {
					return e
				}
			}
			if existed {
				rev := model.Revision{ID: p.ID, JobID: key, DeviceID: p.DeviceID, Key: p.Key, AtMS: p.ObservedMS, Before: previous.Value, After: p.Value, Reason: "late data or historical rule revision", Version: revision}
				if _, e = t.Put("revision", rev.ID, 0, rev); e != nil {
					return e
				}
			}
		}
		if r.Alarm != nil {
			if e = s.updateAlarm(t, d, input, r); e != nil {
				return e
			}
		}
		if r.Trigger && !r.Historical && s.ControlEnabled {
			if e = t.Enqueue("strategy:"+key, "strategy_trigger", r.EntityID, map[string]any{"definition_id": d.ID, "version": d.Version, "input": input, "evaluation": r}); e != nil {
				return e
			}
		}
		return nil
	})
	return outputs, err
}
func (s *Service) updateAlarm(t *store.Tx, d model.Definition, input model.Observation, r Evaluation) error {
	activeID := d.ID + ":" + r.EntityID
	activeKind, alarmKind := "active_alarm", "alarm"
	if r.Historical {
		activeKind, alarmKind = "recompute_alarm_active", "recompute_alarm"
		activeID = r.RunID + ":" + activeID
	}
	var alarm model.Alarm
	if current, e := t.Get(activeKind, activeID); e == nil {
		if e = store.DecodeJSON(current.Data, &alarm); e != nil {
			return e
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	changed := false
	if *r.Alarm {
		if alarm.ID == "" || !alarm.Active {
			alarm = model.Alarm{ID: store.Hash([]any{d.ID, r.EntityID, r.AtMS}), DefinitionID: d.ID, DefinitionVersion: d.Version, EntityID: r.EntityID, Severity: r.Severity, Active: true, StartedMS: r.AtMS, Historical: r.Historical}
			changed = true
		}
		alarm.Count++
		if alarm.Severity != r.Severity {
			changed = true
			alarm.Severity = r.Severity
		}
		alarm.Value = input.Value
		alarm.UpdatedMS = r.AtMS
	} else if alarm.ID != "" && alarm.Active {
		alarm.Active = false
		alarm.ClearedMS = r.AtMS
		alarm.UpdatedMS = r.AtMS
		changed = true
	} else {
		return nil
	}
	alarm.Version++
	alarmID := alarm.ID
	if r.Historical {
		alarmID = r.RunID + ":" + alarmID
	}
	if e := t.SetEphemeral(alarmKind, alarmID, alarm); e != nil {
		return e
	}
	if e := t.SetEphemeral(activeKind, activeID, alarm); e != nil {
		return e
	}
	if !changed || r.Historical {
		return nil
	}
	if e := t.Enqueue(fmt.Sprintf("tb-alarm:%s:%d", alarm.ID, alarm.Version), "tb_alarm", alarm.EntityID, alarm); e != nil {
		return e
	}
	channels := append([]string{}, d.Policy.Channels...)
	if len(channels) == 0 {
		channels = []string{"in_app"}
	}
	if r.Historical {
		switch d.Policy.LateNotification {
		case "silent":
			channels = nil
		case "regular":
		default:
			channels = []string{"in_app"}
		}
	}
	for _, channel := range channels {
		notification := map[string]any{"id": fmt.Sprintf("%s:%d:%s", alarm.ID, alarm.Version, channel), "alarm": alarm, "channel": channel, "recipients": d.Policy.Recipients, "status": "pending"}
		if e := t.Enqueue(fmt.Sprintf("notification:%s:%d:%s", alarm.ID, alarm.Version, channel), "notification", channel, notification); e != nil {
			return e
		}
	}
	return nil
}

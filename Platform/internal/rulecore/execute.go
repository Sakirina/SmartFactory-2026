package rulecore

import (
	"context"
	"errors"
	"fmt"
	"math"

	"competition2026/product/platform/pkg/model"
	"competition2026/product/platform/pkg/precise"
)

type RuntimeState struct {
	Active    bool  `json:"active"`
	Candidate bool  `json:"candidate"`
	SinceMS   int64 `json:"since_ms"`
	Count     int64 `json:"count"`
	Previous  any   `json:"previous"`
	LastMS    int64 `json:"last_ms"`
}

type Frame struct {
	Observation   model.Observation            `json:"observation"`
	Window        []model.Observation          `json:"window"`
	InputFields   map[string]model.Observation `json:"input_fields"`
	ClockMS       int64                        `json:"clock_ms"`
	FreshnessAtMS int64                        `json:"freshness_at_ms"`
	FreshnessMS   int64                        `json:"freshness_ms"`
	EntityID      string                       `json:"entity_id"`
	InitialState  map[string]RuntimeState      `json:"initial_state"`
	AssetVersions []model.AssetVersion         `json:"asset_versions"`
	Historical    bool                         `json:"historical"`
}

type NodeTrace struct {
	NodeID  string       `json:"node_id"`
	Type    string       `json:"type"`
	Input   any          `json:"input"`
	Output  any          `json:"output"`
	Before  RuntimeState `json:"before"`
	After   RuntimeState `json:"after"`
	Skipped bool         `json:"skipped"`
	Error   string       `json:"error,omitempty"`
}

type Evaluation struct {
	DefinitionID  string                  `json:"definition_id"`
	Version       int64                   `json:"version"`
	PlanID        string                  `json:"plan_id"`
	PlanSHA256    string                  `json:"plan_sha256"`
	EntityID      string                  `json:"entity_id"`
	AtMS          int64                   `json:"at_ms"`
	Values        map[string]any          `json:"values"`
	States        map[string]RuntimeState `json:"states"`
	Trigger       bool                    `json:"trigger"`
	Alarm         *bool                   `json:"alarm,omitempty"`
	Severity      string                  `json:"severity"`
	Quality       model.QualitySummary    `json:"quality"`
	Historical    bool                    `json:"historical"`
	RunID         string                  `json:"run_id,omitempty"`
	InputIDs      []string                `json:"input_ids"`
	AssetVersions []model.AssetVersion    `json:"asset_versions"`
	Trace         []NodeTrace             `json:"trace"`
}

// Execute has no store, clock or mutable plan dependency. The caller supplies
// every historical value, clock tick, asset snapshot and initial node state.
func Execute(ctx context.Context, plan *model.ExecutionPlan, frame Frame) (Evaluation, error) {
	executions.Add(1)
	if err := ctx.Err(); err != nil {
		return Evaluation{}, err
	}
	if plan == nil || plan.Format != model.ExecutionPlanFormat {
		return Evaluation{}, errors.New("execution requires a prepared supported plan")
	}
	// Each run owns its complete state and JSON inputs, including composite
	// previous values. Mutating a returned result cannot alter a later run.
	var err error
	frame, err = clone(frame)
	if err != nil {
		return Evaluation{}, fmt.Errorf("invalid execution frame: %w", err)
	}
	point := frame.Observation
	result := Evaluation{DefinitionID: plan.DefinitionID, Version: plan.DefinitionVersion, PlanID: plan.ID, PlanSHA256: plan.SHA256, EntityID: frame.EntityID, AtMS: frame.ClockMS, Values: map[string]any{}, States: map[string]RuntimeState{}, Historical: frame.Historical, Severity: "WARNING", Quality: model.QualitySummary{Completeness: "unknown"}, InputIDs: []string{}, AssetVersions: append([]model.AssetVersion{}, frame.AssetVersions...), Trace: []NodeTrace{}}
	if result.EntityID == "" {
		result.EntityID = point.DeviceID
	}
	for id, state := range frame.InitialState {
		result.States[id] = state
	}
	for _, item := range frame.Window {
		result.InputIDs = append(result.InputIDs, item.ID)
		switch item.Quality {
		case "GOOD":
			result.Quality.Good++
		case "BAD":
			result.Quality.Bad++
			result.Quality.Excluded++
		case "UNCERTAIN":
			result.Quality.Uncertain++
			result.Quality.Excluded++
		}
	}
	aggregate := precise.AggregatePoints(frame.Window)
	for _, node := range plan.Nodes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		value := point.Value
		enabled := true
		for _, connection := range node.Inputs {
			key := connection.From
			if connection.FromPort == "error" {
				key += ".error"
			}
			upstream, exists := result.Values[key]
			if !exists || connection.FromPort == "true" && !Truth(upstream) || connection.FromPort == "false" && Truth(upstream) {
				enabled = false
				break
			}
			value = upstream
		}
		state := frame.InitialState[node.ID]
		trace := NodeTrace{NodeID: node.ID, Type: node.Type, Input: value, Before: state, After: state, Skipped: !enabled}
		if !enabled {
			result.Trace = append(result.Trace, trace)
			continue
		}
		age := frame.FreshnessAtMS - point.ObservedMS
		vars := map[string]any{"value": value, "sum": aggregate.Sum, "count": aggregate.Count, "min": aggregate.Min, "max": aggregate.Max, "avg": nil, "previous": state.Previous, "active": state.Active, "good": point.Quality == "GOOD", "quality": point.Quality, "fresh": age >= 0 && age <= frame.FreshnessMS}
		if aggregate.Average != nil {
			vars["avg"] = *aggregate.Average
		}
		if vars["previous"] == nil {
			vars["previous"] = 0
		}
		var err error
		switch node.Type {
		case "input":
			if key := node.Params.Key; key != "" && key != point.Key {
				input, exists := frame.InputFields[key]
				if !exists {
					return result, fmt.Errorf("input %s is missing", key)
				}
				value = input.Value
			}
		case "aggregate":
			unit := ""
			for _, item := range frame.Window {
				if item.Quality == "GOOD" && item.Unit != "" {
					if unit != "" && item.Unit != unit {
						return result, fmt.Errorf("node %s aggregate window mixes units", node.ID)
					}
					unit = item.Unit
				}
			}
			if aggregate.Count == 0 {
				value = nil
			} else {
				switch node.Params.Function {
				case "sum":
					value = aggregate.Sum
				case "count":
					value = aggregate.Count
				case "min":
					value = aggregate.Min
				case "max":
					value = aggregate.Max
				case "avg":
					value = *aggregate.Average
				}
			}
		case "expression":
			if plan.Kind == "analysis" && result.Quality.Good == 0 {
				value = nil
				break
			}
			value, err = EvaluateExpression(ctx, node.Expression, vars)
			if err != nil {
				trace.Error = err.Error()
				result.Trace = append(result.Trace, trace)
				if !node.ErrorOutput {
					return result, fmt.Errorf("expression %s: %w", node.ID, err)
				}
				result.Values[node.ID+".error"] = err.Error()
				continue
			}
		case "threshold", "condition", "branch":
			value, err = Compare(value, node.Params.Value, node.Params.Operator)
			if point.Quality != "GOOD" {
				value = false
			}
		case "hysteresis":
			if point.Quality != "GOOD" {
				value = state.Active
				break
			}
			high, highErr := Compare(value, node.Params.High, ">=")
			low, lowErr := Compare(value, node.Params.Low, "<=")
			err = errors.Join(highErr, lowErr)
			if node.Params.Direction == "below" {
				if low {
					state.Active = true
				} else if high {
					state.Active = false
				}
			} else if high {
				state.Active = true
			} else if low {
				state.Active = false
			}
			value = state.Active
		case "debounce":
			candidate := Truth(value)
			if node.Params.Mode == "activation" && !candidate {
				state.Candidate, state.Active, state.SinceMS = false, false, 0
				value = false
				break
			}
			if state.SinceMS == 0 || candidate != state.Candidate {
				state.Candidate, state.SinceMS = candidate, frame.ClockMS
			}
			if frame.ClockMS-state.SinceMS >= node.Params.DurationMS {
				state.Active = candidate
			}
			value = state.Active
		case "counter":
			if point.Quality == "GOOD" {
				increment := int64(1)
				if node.Params.Mode == "delta" {
					number, ok := precise.Number(value)
					if !ok || !number.IsInt() || !number.Num().IsInt64() {
						return result, fmt.Errorf("node %s counter delta requires an int64 value", node.ID)
					}
					increment = number.Num().Int64()
				}
				if node.Params.Mode != "rising" || Truth(value) && !Truth(state.Previous) {
					if increment > 0 && state.Count > math.MaxInt64-increment || increment < 0 && state.Count < math.MinInt64-increment {
						return result, fmt.Errorf("node %s counter exceeds int64 range", node.ID)
					}
					state.Count += increment
				}
				state.Previous = value
			}
			value = state.Count
		case "alarm":
			if point.Quality != "GOOD" {
				trace.Skipped = true
				result.Trace = append(result.Trace, trace)
				continue
			}
			active := Truth(value)
			result.Alarm = &active
			result.Severity = node.Params.Severity
		case "action":
			result.Trigger = result.Trigger || Truth(value) && !state.Active
			state.Active = Truth(value)
		case "output":
		default:
			return result, fmt.Errorf("unsupported prepared node %s", node.Type)
		}
		if err != nil {
			trace.Error = err.Error()
			result.Trace = append(result.Trace, trace)
			return result, fmt.Errorf("node %s: %w", node.ID, err)
		}
		state.LastMS = frame.ClockMS
		if node.Type != "counter" {
			state.Previous = value
		}
		result.States[node.ID], result.Values[node.ID] = state, value
		trace.After, trace.Output = state, value
		result.Trace = append(result.Trace, trace)
	}
	return result, nil
}

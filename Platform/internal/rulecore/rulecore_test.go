package rulecore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"competition2026/product/platform/pkg/model"
)

func coreDefinition(code string) model.Definition {
	return model.Definition{ID: "rule", Name: "Rule", Kind: "analysis", SchemaVersion: model.ContractVersion, Status: "published", Version: 7, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"value"}}, Nodes: []model.Node{{ID: "calculate", Type: "expression", Params: map[string]any{"code": code}}}, Outputs: []model.Output{{NodeID: "calculate", Key: "result", Type: "number"}}}
}

func corePlan(t *testing.T, definition model.Definition) *model.ExecutionPlan {
	t.Helper()
	plan, validation := Compile(context.Background(), definition)
	if !validation.Valid {
		t.Fatal(validation.Errors)
	}
	return plan
}

func coreFrame(value any) Frame {
	point := model.Observation{ID: "input", DeviceID: "device", Key: "value", Value: value, Quality: "GOOD", Unit: "piece", ObservedMS: 1000, SourceID: "edge", SourceSequence: 2}
	return Frame{Observation: point, Window: []model.Observation{point}, ClockMS: 1000, FreshnessAtMS: 1000, FreshnessMS: 5000, EntityID: "device", InitialState: map[string]RuntimeState{}, AssetVersions: []model.AssetVersion{{ID: "device", Version: 3, EffectiveMS: 900, ParentID: "asset", SamplingMS: 1000}}}
}

func TestPlanSerializationIdentityAndExecutionReuse(t *testing.T) {
	definition := coreDefinition("value + 1")
	plan := corePlan(t, definition)
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var restored model.ExecutionPlan
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&restored); err != nil {
		t.Fatal(err)
	}
	if err := Verify(&restored, definition); err != nil {
		t.Fatal(err)
	}
	before := SnapshotStatistics()
	frame := coreFrame(json.Number("9007199254740993"))
	first, err := Execute(context.Background(), &restored, frame)
	if err != nil || fmt.Sprint(first.Values["calculate"]) != "9007199254740994" || !reflect.DeepEqual(first.AssetVersions, frame.AssetVersions) {
		t.Fatalf("precise value and assets: %+v %v", first, err)
	}
	for range 50 {
		again, err := Execute(context.Background(), &restored, frame)
		if err != nil || !reflect.DeepEqual(first, again) {
			t.Fatalf("nondeterministic execution: %+v %v", again, err)
		}
	}
	after := SnapshotStatistics()
	t.Logf("serialized plan across 51 executions: compilations=%d expression_parses=%d executions=%d", after.Compilations-before.Compilations, after.ExpressionParses-before.ExpressionParses, after.Executions-before.Executions)
	if after.Compilations != before.Compilations || after.ExpressionParses != before.ExpressionParses || after.Executions-before.Executions != 51 {
		t.Fatalf("runtime reparsed: before=%+v after=%+v", before, after)
	}
	definition.Nodes[0].Params["code"] = "0"
	if err := Verify(plan, definition); err == nil {
		t.Fatal("changed definition accepted")
	}
	restored.Nodes[0].Expression.Op = "literal"
	if err := Verify(&restored, coreDefinition("value + 1")); err == nil {
		t.Fatal("corrupt serialized instruction accepted")
	}
}

func TestIndependentStateIncludesCompositePreviousValues(t *testing.T) {
	definition := coreDefinition("value")
	definition.Nodes[0] = model.Node{ID: "calculate", Type: "input"}
	plan := corePlan(t, definition)
	frame := coreFrame(map[string]any{"original": "kept"})
	frame.InitialState["unused"] = RuntimeState{Previous: map[string]any{"original": "state"}, Count: 9}
	first, err := Execute(context.Background(), plan, frame)
	if err != nil {
		t.Fatal(err)
	}
	first.States["unused"].Previous.(map[string]any)["original"] = "changed"
	first.Values["calculate"].(map[string]any)["original"] = "changed"
	second, err := Execute(context.Background(), plan, frame)
	if err != nil || second.States["unused"].Previous.(map[string]any)["original"] != "state" || second.Values["calculate"].(map[string]any)["original"] != "kept" {
		t.Fatalf("state shared between runs: %+v %v", second, err)
	}
}

func TestCounterPrecisionQualityAndOverflow(t *testing.T) {
	definition := coreDefinition("value")
	definition.Nodes[0] = model.Node{ID: "calculate", Type: "counter", Params: map[string]any{"mode": "delta"}}
	plan := corePlan(t, definition)
	frame := coreFrame(json.Number("9007199254740993"))
	first, err := Execute(context.Background(), plan, frame)
	if err != nil || first.States["calculate"].Count != 9007199254740993 {
		t.Fatal(first, err)
	}
	frame.InitialState = first.States
	frame.Observation.Quality = "BAD"
	frame.Observation.Value = 500
	bad, err := Execute(context.Background(), plan, frame)
	if err != nil || bad.States["calculate"].Count != 9007199254740993 {
		t.Fatal("bad quality changed count", bad, err)
	}
	frame.Observation.Quality = "GOOD"
	frame.InitialState["calculate"] = RuntimeState{Count: math.MaxInt64}
	frame.Observation.Value = 1
	if _, err := Execute(context.Background(), plan, frame); err == nil || !strings.Contains(err.Error(), "int64 range") {
		t.Fatal("overflow accepted", err)
	}
	frame.InitialState = nil
	for _, value := range []any{1.5, json.Number("9223372036854775808"), "2"} {
		frame.Observation.Value = value
		if _, err := Execute(context.Background(), plan, frame); err == nil {
			t.Fatalf("invalid counter delta %v accepted", value)
		}
	}
}

func TestWindowPrecisionQualityUnitsAndFreshness(t *testing.T) {
	definition := coreDefinition("sum")
	definition.Nodes[0] = model.Node{ID: "calculate", Type: "aggregate", Params: map[string]any{"function": "sum"}}
	plan := corePlan(t, definition)
	frame := coreFrame(json.Number("9007199254740993"))
	second := frame.Observation
	second.ID = "second"
	second.Value = json.Number("9007199254740993")
	bad := frame.Observation
	bad.ID = "bad"
	bad.Value = 999
	bad.Quality = "BAD"
	bad.Unit = "other"
	frame.Window = append(frame.Window, second, bad)
	result, err := Execute(context.Background(), plan, frame)
	if err != nil || fmt.Sprint(result.Values["calculate"]) != "18014398509481986" || result.Quality.Good != 2 || result.Quality.Bad != 1 || result.Quality.Excluded != 1 || len(result.InputIDs) != 3 {
		t.Fatal(result, err)
	}
	frame.Window[1].Unit = "kg"
	if _, err := Execute(context.Background(), plan, frame); err == nil || !strings.Contains(err.Error(), "mixes units") {
		t.Fatal("mixed units accepted", err)
	}
	freshPlan := corePlan(t, coreDefinition("fresh"))
	frame = coreFrame(3)
	for _, sample := range []struct {
		at   int64
		want bool
	}{{999, false}, {1000, true}, {6000, true}, {6001, false}} {
		frame.FreshnessAtMS = sample.at
		result, err := Execute(context.Background(), freshPlan, frame)
		if err != nil || result.Values["calculate"] != sample.want {
			t.Fatalf("freshness %d: %+v %v", sample.at, result, err)
		}
	}
}

func TestCompilationLocatesParametersTypesPortsAndExpressionBudget(t *testing.T) {
	for _, code := range []string{"missing + 1", `true + 1`, `choose(1,2,3)`, strings.Repeat("1+", 2048) + "1"} {
		_, result := Compile(context.Background(), coreDefinition(code))
		if result.Valid || !strings.Contains(strings.Join(result.Errors, " "), "node calculate params.code") {
			t.Fatal(code, result)
		}
	}
	definition := coreDefinition("value")
	definition.Kind, definition.Outputs = "alarm", nil
	definition.Nodes = []model.Node{{ID: "delay", Type: "debounce", Params: map[string]any{"duration_ms": 0}}}
	if _, result := Compile(context.Background(), definition); result.Valid || !strings.Contains(strings.Join(result.Errors, " "), "params.duration_ms") {
		t.Fatal(result)
	}
	definition.Nodes[0].Params["duration_ms"] = 10
	plan := corePlan(t, definition)
	if plan.Nodes[0].Params.Mode != "both" {
		t.Fatal("metadata default missing", plan)
	}
	definition.Connections = []model.Connection{{From: "delay", FromPort: "absent", To: "delay"}}
	if _, result := Compile(context.Background(), definition); result.Valid {
		t.Fatal("invalid port accepted")
	}
}

type stepBudgetContext struct {
	context.Context
	remaining int
}

func (c *stepBudgetContext) Err() error {
	c.remaining--
	if c.remaining < 0 {
		return context.DeadlineExceeded
	}
	return nil
}

func TestExecutionSharesOneGraphDeadlineAcrossExpressions(t *testing.T) {
	definition := coreDefinition("value + 1")
	definition.Nodes = append(definition.Nodes, model.Node{ID: "second", Type: "expression", Params: map[string]any{"code": "value + 1"}})
	definition.Connections = []model.Connection{{From: "calculate", To: "second"}}
	plan := corePlan(t, definition)
	ctx := &stepBudgetContext{Context: context.Background(), remaining: 6}
	result, err := Execute(ctx, plan, coreFrame(1))
	if !errors.Is(err, context.DeadlineExceeded) || len(result.Trace) < 1 || fmt.Sprint(result.Values["calculate"]) != "2" {
		t.Fatalf("graph deadline did not span expressions: %+v %v", result, err)
	}
	if _, exists := result.Values["second"]; exists {
		t.Fatal("second expression reset the shared graph budget")
	}
}

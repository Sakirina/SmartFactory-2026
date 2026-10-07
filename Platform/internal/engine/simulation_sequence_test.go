package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func simulationPoint(id string, at int64, value any) model.Observation {
	return model.Observation{ID: id, DeviceID: "device", Key: "temperature", ObservedMS: at, Value: value, Quality: "GOOD", Unit: "piece", SourceID: "edge-a"}
}

func TestSequenceSimulationCompilesOnceContinuesIndependentEntityState(t *testing.T) {
	s := engineFixture(t)
	ctx := context.Background()
	d := definition()
	d.Selector.WindowMS = 0
	d.Selector.DeviceIDs = []string{"device", "second"}
	d.Nodes = []model.Node{{ID: "expression", Type: "expression", Params: map[string]any{"code": "value + 1"}}, {ID: "counter", Type: "counter", Params: map[string]any{"mode": "delta"}}}
	d.Connections = []model.Connection{{From: "expression", To: "counter"}}
	d.Outputs = nil
	points := []model.Observation{simulationPoint("b", 1000, 1), simulationPoint("a", 1000, 2), simulationPoint("c", 2000, 3)}
	points[0].SourceSequence = 2
	points[1].SourceSequence = 1
	points[2].DeviceID = "second"
	initial := map[string]map[string]RuntimeState{"device": {"counter": {Count: 10}}, "second": {"counter": {Count: 100}}}
	request := SimulationRequest{Points: points, History: []model.Observation{}, InitialState: initial}
	before := rulecore.SnapshotStatistics()
	result, err := s.SimulateSequence(ctx, d, request)
	after := rulecore.SnapshotStatistics()
	t.Logf("three inputs: compilations=%d expression_parses=%d executions=%d", after.Compilations-before.Compilations, after.ExpressionParses-before.ExpressionParses, after.Executions-before.Executions)
	if err != nil {
		t.Fatal(err)
	}
	if after.Compilations-before.Compilations != 1 || after.ExpressionParses-before.ExpressionParses != 1 || after.Executions-before.Executions != 3 {
		t.Fatal("sequence compilation count", before, after)
	}
	if result.Results[0].Point.ID != "a" || result.Results[0].InputIndex != 1 || result.Results[0].Evaluation.States["counter"].Count != 13 || result.Results[1].Evaluation.States["counter"].Count != 15 || result.FinalState["second"]["counter"].Count != 104 {
		t.Fatalf("state/order: %+v", result)
	}
	if initial["device"]["counter"].Count != 10 {
		t.Fatal("request state mutated")
	}
	repeat, err := s.SimulateSequence(ctx, d, request)
	if err != nil || !reflect.DeepEqual(result, repeat) {
		t.Fatal("same explicit simulation differed", err)
	}
	for _, kind := range []string{"engine_state", "recompute_state", "active_alarm", "alarm", "execution", "compiled_plan"} {
		items, err := s.Store.List(ctx, kind)
		if err != nil || len(items) != 0 {
			t.Fatal("simulation wrote business state", kind, items, err)
		}
	}
	var jobs int
	if err := s.Store.DB.QueryRow("SELECT count(*) FROM outbox").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("simulation created actions", jobs, err)
	}
}

func TestSequenceWindowHistorySnapshotAndExactValues(t *testing.T) {
	s := engineFixture(t)
	s.Store.Now = func() time.Time { return time.UnixMilli(4000) }
	ctx := context.Background()
	d := definition()
	d.Selector.WindowMS = 1000
	d.Nodes[1].Params["function"] = "sum"
	points := []model.Observation{simulationPoint("third", 3000, 5), simulationPoint("first", 2000, json.Number("9007199254740993")), simulationPoint("second", 2500, 2)}
	history := []model.Observation{simulationPoint("expired", 1000, 99), simulationPoint("history", 1501, 3)}
	result, err := s.SimulateSequence(ctx, d, SimulationRequest{Points: points, History: history})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"9007199254740996", "9007199254740998", "7"}
	for i, value := range want {
		if fmt.Sprint(result.Results[i].Evaluation.Values["result"]) != value {
			t.Fatalf("window %d: %+v", i, result.Results[i])
		}
	}
	if result.HistorySource != "provided" || result.HistorySHA256 != store.Hash(result.History) || result.InputSHA256 == "" || result.PlanSHA256 == "" {
		t.Fatal("missing reproducibility identity", result)
	}
	stored := simulationPoint("stored", 1900, 7)
	if _, err := s.Store.Ingest(ctx, store.IngestBatch{MessageID: "stored", SourceID: "edge-a", Points: []model.Observation{stored}}); err != nil {
		t.Fatal(err)
	}
	point := simulationPoint("sample", 2000, 1)
	explicit, err := s.SimulateSequence(ctx, d, SimulationRequest{Points: []model.Observation{point}, History: []model.Observation{}})
	if err != nil || fmt.Sprint(explicit.Results[0].Evaluation.Values["result"]) != "1" {
		t.Fatal(explicit, err)
	}
	implicit, err := s.SimulateSequence(ctx, d, SimulationRequest{Points: []model.Observation{point}})
	if err != nil || fmt.Sprint(implicit.Results[0].Evaluation.Values["result"]) != "8" || implicit.HistorySource != "database_snapshot" || implicit.HistorySHA256 == explicit.HistorySHA256 {
		t.Fatal(implicit, err)
	}
	replay, err := s.SimulateSequence(ctx, d, SimulationRequest{Points: []model.Observation{point}, History: implicit.History, AssetVersions: implicit.AssetVersions})
	if err != nil || replay.InputSHA256 != implicit.InputSHA256 || !reflect.DeepEqual(replay.Results, implicit.Results) {
		t.Fatal("snapshot cannot reproduce result", replay, err)
	}
}

func TestSequenceExplicitClockMissingInputAndHistoricalAssets(t *testing.T) {
	s := engineFixture(t)
	ctx := context.Background()
	d := definition()
	d.Selector.WindowMS = 0
	d.Selector.DeviceIDs = nil
	d.Selector.AssetID = "first-asset"
	d.Nodes = []model.Node{{ID: "fresh", Type: "expression", Params: map[string]any{"code": "fresh"}}}
	d.Connections = nil
	d.Outputs = nil
	assets := []model.AssetVersion{{ID: "device", Version: 1, EffectiveMS: 100, ParentID: "first-asset", SamplingMS: 1000}, {ID: "device", Version: 2, EffectiveMS: 2000, ParentID: "second-asset", SamplingMS: 2000}}
	clock := int64(8000)
	result, err := s.SimulateSequence(ctx, d, SimulationRequest{Points: []model.Observation{simulationPoint("before-move", 1500, 1)}, History: []model.Observation{}, AssetVersions: assets, Clock: SimulationClock{AtMS: &clock}})
	if err != nil || result.Results[0].Evaluation.Values["fresh"] != false || result.Results[0].Evaluation.AssetVersions[0].Version != 1 || result.Results[0].Evaluation.EntityID != "first-asset" {
		t.Fatal(result, err)
	}
	if _, err := s.SimulateSequence(ctx, d, SimulationRequest{Points: []model.Observation{simulationPoint("after-move", 2500, 1)}, History: []model.Observation{}, AssetVersions: assets}); err == nil {
		t.Fatal("historical asset movement ignored")
	}
	d.Selector.AssetID = ""
	d.Selector.DeviceIDs = []string{"device"}
	d.Nodes = []model.Node{{ID: "alternate", Type: "input", Params: map[string]any{"key": "missing"}}}
	if _, err := s.SimulateSequence(ctx, d, SimulationRequest{Points: []model.Observation{simulationPoint("missing", 1500, 1)}, History: []model.Observation{}}); err == nil {
		t.Fatal("missing input accepted")
	}
	history := simulationPoint("alternate", 1000, 42)
	history.Key = "missing"
	result, err = s.SimulateSequence(ctx, d, SimulationRequest{Points: []model.Observation{simulationPoint("present", 1500, 1)}, History: []model.Observation{history}})
	if err != nil || fmt.Sprint(result.Results[0].Evaluation.Values["alternate"]) != "42" {
		t.Fatal(result, err)
	}
}

func TestSequenceBudgetsCancellationAndGivenOrder(t *testing.T) {
	s := engineFixture(t)
	ctx := context.Background()
	d := definition()
	d.Selector.WindowMS = 0
	d.Nodes[1].Type = "counter"
	d.Nodes[1].Params = map[string]any{"mode": "delta"}
	points := []model.Observation{simulationPoint("b", 2000, 2), simulationPoint("a", 1000, 3)}
	request := SimulationRequest{Points: points, History: []model.Observation{}, Order: "provided"}
	result, err := s.SimulateSequence(ctx, d, request)
	if err != nil || result.Results[0].Point.ID != "b" || result.FinalState["device"]["sum"].Count != 5 {
		t.Fatal(result, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.SimulateSequence(cancelled, d, request); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
	request.Points = make([]model.Observation, SimulationMaxPoints+1)
	if _, err := s.SimulateSequence(ctx, d, request); err == nil {
		t.Fatal("point budget ignored")
	}
	request.Points = points
	request.History = make([]model.Observation, SimulationMaxHistory+1)
	if _, err := s.SimulateSequence(ctx, d, request); err == nil {
		t.Fatal("history budget ignored")
	}
	request.History = nil
	request.TimeoutMS = SimulationTimeoutMS + 1
	if _, err := s.SimulateSequence(ctx, d, request); err == nil {
		t.Fatal("time budget ignored")
	}
}

func TestRealtimeAndSimulationUseSamePreparedCoreWithoutParsing(t *testing.T) {
	s := engineFixture(t)
	ctx := context.Background()
	d := definition()
	d.Selector.WindowMS = 0
	d.Nodes[1] = model.Node{ID: "sum", Type: "expression", Params: map[string]any{"code": "value + 1"}}
	if _, err := s.Store.Put(ctx, "definition", d.ID, 0, d); err != nil {
		t.Fatal(err)
	}
	prepareLegacyFixture(t, s)
	before := rulecore.SnapshotStatistics()
	points := []model.Observation{}
	for i := range 20 {
		point := simulationPoint(fmt.Sprint(i), 1000+int64(i), json.Number("9007199254740993"))
		points = append(points, point)
		if err := s.ProcessCalculations(ctx, point, false); err != nil {
			t.Fatal(err)
		}
	}
	after := rulecore.SnapshotStatistics()
	t.Logf("twenty live inputs: compilations=%d expression_parses=%d executions=%d", after.Compilations-before.Compilations, after.ExpressionParses-before.ExpressionParses, after.Executions-before.Executions)
	if after.Compilations != before.Compilations || after.ExpressionParses != before.ExpressionParses || after.Executions-before.Executions != 20 {
		t.Fatal("production hot path parsed expressions", before, after)
	}
	result, err := s.SimulateSequence(ctx, d, SimulationRequest{Points: points, History: []model.Observation{}})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.Store.Latest(ctx, "device", "mean.mean")
	if err != nil || fmt.Sprint(latest.Value) != fmt.Sprint(result.Results[len(points)-1].Evaluation.Values["result"]) || fmt.Sprint(latest.Value) != "9007199254740994" {
		t.Fatal("runtime/simulation mismatch", latest, result, err)
	}
}

func TestExplicitSequenceRequiresNoDatabase(t *testing.T) {
	s := &Service{}
	d := definition()
	result, err := s.SimulateSequence(context.Background(), d, SimulationRequest{Points: []model.Observation{simulationPoint("explicit", 1000, 2)}, History: []model.Observation{}, AssetVersions: []model.AssetVersion{}})
	if err != nil || fmt.Sprint(result.Results[0].Evaluation.Values["result"]) != "2" {
		t.Fatal(result, err)
	}
}

func TestSimulationRequestRoundTripRetainsExplicitEmptySnapshots(t *testing.T) {
	request := SimulationRequest{Points: []model.Observation{simulationPoint("explicit", 1000, 2)}, History: []model.Observation{}, AssetVersions: []model.AssetVersion{}}
	data, err := json.Marshal(request)
	if err != nil || !strings.Contains(string(data), `"history":[]`) || !strings.Contains(string(data), `"asset_versions":[]`) {
		t.Fatal("explicit snapshot omitted", string(data), err)
	}
	var restored SimulationRequest
	if err := store.DecodeJSON(data, &restored); err != nil || restored.History == nil || restored.AssetVersions == nil {
		t.Fatal(restored, err)
	}
	request.History = nil
	data, err = json.Marshal(request)
	if err != nil || strings.Contains(string(data), `"history"`) {
		t.Fatal("implicit snapshot became explicit", string(data), err)
	}
}

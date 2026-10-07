package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/tasks"
	"competition2026/product/platform/pkg/model"
	"go.opentelemetry.io/otel"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestHistoryHTTPTasksExactValuesAuthorizationAndOTLP(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	spans := []*tracepb.Span{}
	collectorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var request collector.ExportTraceServiceRequest
		if err := proto.Unmarshal(raw, &request); err != nil {
			http.Error(w, "invalid protobuf", 400)
			return
		}
		mu.Lock()
		for _, resource := range request.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				spans = append(spans, scope.Spans...)
			}
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer collectorServer.Close()
	previousTrace, previousMeter, previousPropagation := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	runtime, err := observability.New(ctx, observability.Config{ServiceName: "history-http-verification", TraceExporter: "otlp", Endpoint: collectorServer.URL, Sampler: "always_on", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Install()
	defer func() {
		_ = runtime.Shutdown(ctx)
		otel.SetTracerProvider(previousTrace)
		otel.SetMeterProvider(previousMeter)
		otel.SetTextMapPropagator(previousPropagation)
	}()
	s, token := scopedServer(t, false)
	base := time.Now().UnixMilli() - 10000
	rule := model.Definition{ID: "http-comparison", Name: "HTTP comparison", SchemaVersion: model.ContractVersion, Kind: "analysis", GroupID: "a", Status: "published", Version: 1, EffectiveMS: base, Selector: model.Selector{DeviceIDs: []string{"device-a"}, Keys: []string{"count"}}, Nodes: []model.Node{{ID: "value", Type: "expression", Params: map[string]any{"code": "value"}}}, Outputs: []model.Output{{NodeID: "value", Key: "count", Type: "number"}}}
	publish := func(d model.Definition) {
		t.Helper()
		if _, e := s.Store.Put(ctx, "definition", d.ID, d.Version-1, d); e != nil {
			t.Fatal(e)
		}
		if e := s.Engine.PrepareDefinition(ctx, d); e != nil {
			t.Fatal(e)
		}
	}
	publish(rule)
	rule.Version, rule.EffectiveMS = 2, base+1
	rule.Nodes[0].Params = map[string]any{"code": "value+1"}
	publish(rule)
	failed := rule
	failed.ID, failed.Version = "http-failure", 1
	failed.Nodes = []model.Node{{ID: "value", Type: "input", Params: map[string]any{"key": "missing_field"}}}
	publish(failed)
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	responses := map[string]json.RawMessage{}
	request := func(method, path string, body any, status int, name string) []byte {
		t.Helper()
		raw, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		req, e := http.NewRequest(method, server.URL+"/api/sf/v1"+path, bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
		req.Header.Set("X-Request-ID", "history-http-request")
		response, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		result, _ := io.ReadAll(response.Body)
		if response.StatusCode != status {
			t.Fatalf("%s %s: %d expected %d: %s", method, path, response.StatusCode, status, result)
		}
		if name != "" {
			responses[name] = result
		}
		return result
	}
	point := model.Observation{ID: "http-input", MessageID: "http-message", DeviceID: "device-a", Key: "count", Value: json.Number("9007199254740993"), ObservedMS: base + 100, ReceivedMS: base + 101, Quality: "GOOD", Revision: 1}
	input := historymodel.Request{ID: "http-compare", Kind: "compare", DefinitionID: rule.ID, LeftVersion: 1, RightVersion: 2, FromMS: base + 100, ToMS: base + 100, Points: []model.Observation{point}, History: []model.Observation{}}
	var run historymodel.Run
	if err = store.DecodeJSON(request("POST", "/analysis-runs", input, 202, "created_run"), &run); err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err = store.DecodeJSON(request("GET", "/tasks/"+run.TaskID, nil, 200, "original_task"), &task); err != nil {
		t.Fatal(err)
	}
	request("POST", "/tasks/"+task.ID+"/cancel", model.TaskAction{ExpectedVersion: "stale"}, 409, "stale_task_error")
	request("POST", "/tasks/"+task.ID+"/cancel", model.TaskAction{ExpectedVersion: task.Version}, 200, "cancelled_task")
	request("GET", "/analysis-runs/"+run.ID, nil, 200, "cancelled_run")
	if err = store.DecodeJSON(request("GET", "/tasks/"+run.TaskID, nil, 200, ""), &task); err != nil {
		t.Fatal(err)
	}
	request("POST", "/tasks/"+task.ID+"/retry", model.TaskAction{ExpectedVersion: task.Version}, 200, "retried_task")
	bad := input
	bad.ID, bad.DefinitionID = "missing-rule", "not-found"
	request("POST", "/analysis-runs", bad, 404, "create_error")
	bad.ID, bad.DefinitionID, bad.Kind = "execution-failure", failed.ID, "replay"
	bad.LeftVersion, bad.RightVersion = 0, 0
	var failure historymodel.Run
	if err = store.DecodeJSON(request("POST", "/analysis-runs", bad, 202, "failure_created"), &failure); err != nil {
		t.Fatal(err)
	}
	worker, err := tasks.New(s.Store, tasks.Handlers{Analysis: s.HistoryApplication().Execute})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = worker.Stop(stop)
	}()
	deadline := time.Now().Add(10 * time.Second)
	var current, failureRun historymodel.Run
	for time.Now().Before(deadline) {
		current, _ = s.Store.AnalysisRun(ctx, run.ID)
		failureRun, _ = s.Store.AnalysisRun(ctx, failure.ID)
		if current.Status == "completed" && failureRun.Status == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if current.Status != "completed" || failureRun.Status != "failed" || current.SnapshotSHA256 != run.SnapshotSHA256 {
		t.Fatal(current, failureRun)
	}
	request("GET", "/analysis-runs/"+run.ID, nil, 200, "completed_run")
	request("GET", "/analysis-runs/"+failure.ID, nil, 200, "failed_run")
	snapshot := request("GET", "/analysis-runs/"+run.ID+"/snapshot", nil, 200, "snapshot")
	steps := request("GET", "/analysis-runs/"+run.ID+"/steps?limit=1", nil, 200, "steps")
	if !bytes.Contains(snapshot, []byte("9007199254740993")) || !bytes.Contains(steps, []byte("9007199254740994")) {
		t.Fatal("HTTP numeric precision was lost", string(snapshot), string(steps))
	}
	request("GET", "/analysis-runs?limit=1", nil, 200, "run_page")
	var candidate historymodel.Candidate
	if err = store.DecodeJSON(request("POST", "/shadow-candidates", historymodel.ShadowRequest{ID: "http-candidate", DefinitionID: rule.ID, Version: 1}, 200, "candidate"), &candidate); err != nil {
		t.Fatal(err)
	}
	request("POST", "/shadow-candidates/http-candidate/stop", historymodel.CandidateAction{ExpectedVersion: candidate.Version}, 200, "stopped_candidate")
	userDoc, err := s.Store.Get(ctx, "user", "user")
	if err != nil {
		t.Fatal(err)
	}
	user, _ := store.Decode[model.User](userDoc)
	user.Resources = []string{"b"}
	user.Version = userDoc.Version + 1
	if _, err = s.Store.Put(ctx, "user", user.ID, userDoc.Version, user); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/snapshot", "/steps"} {
		request("GET", "/analysis-runs/"+run.ID+suffix, nil, 403, "authorization"+strings.ReplaceAll(suffix, "/", "_"))
	}
	if err = runtime.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	capturedSpans := append([]*tracepb.Span{}, spans...)
	mu.Unlock()
	var evaluated *tracepb.Span
	var createError, runError, linkedCommit bool
	for _, span := range capturedSpans {
		if span.Name == "analysis.create" && span.Status.GetCode() == tracepb.Status_STATUS_CODE_ERROR {
			createError = true
		}
		if span.Name == "analysis.run" && span.Status.GetCode() == tracepb.Status_STATUS_CODE_ERROR {
			runError = true
		}
		if span.Name == "analysis.evaluate" && historyTraceAttribute(span, "smartfactory.run_id").GetStringValue() == run.ID {
			evaluated = span
		}
	}
	if evaluated != nil {
		for _, span := range capturedSpans {
			if span.Name == "store.transaction" && bytes.Equal(span.ParentSpanId, evaluated.SpanId) {
				linkedCommit = true
			}
		}
	}
	if evaluated == nil || hex.EncodeToString(evaluated.TraceId) != "11111111111111111111111111111111" || len(historyTraceAttribute(evaluated, "smartfactory.plan_ids").GetArrayValue().GetValues()) != 2 || len(historyTraceAttribute(evaluated, "smartfactory.definition_versions").GetArrayValue().GetValues()) != 2 || len(historyTraceAttribute(evaluated, "smartfactory.analysis_sides").GetArrayValue().GetValues()) != 2 || !createError || !runError || !linkedCommit {
		t.Fatalf("OTLP actual exported spans: comparison=%v create_error=%v run_error=%v transaction_child=%v total=%d", evaluated, createError, runError, linkedCommit, len(capturedSpans))
	}
	if path := os.Getenv("SF_HISTORY_HTTP_SAMPLE_OUTPUT"); path != "" {
		raw, _ := json.MarshalIndent(responses, "", "  ")
		if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("real HTTP compare=202 cancel=200 stale=409 retry=200 completed exact integer=9007199254740994; read/snapshot/steps revoked=403; OTLP spans=%d trace=%s plans=%v versions=%v sides=%v transaction_parent=%s create_failure=ERROR run_failure=ERROR", len(capturedSpans), hex.EncodeToString(evaluated.TraceId), historyTraceAttribute(evaluated, "smartfactory.plan_ids").GetArrayValue(), historyTraceAttribute(evaluated, "smartfactory.definition_versions").GetArrayValue(), historyTraceAttribute(evaluated, "smartfactory.analysis_sides").GetArrayValue(), hex.EncodeToString(evaluated.SpanId))
}

func historyTraceAttribute(span *tracepb.Span, key string) *common.AnyValue {
	for _, attribute := range span.Attributes {
		if attribute.Key == key {
			return attribute.Value
		}
	}
	return nil
}

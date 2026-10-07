package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func testRuntime(t *testing.T, ratio float64) (*Runtime, *tracetest.InMemoryExporter, *sdkmetric.ManualReader) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	reader := sdkmetric.NewManualReader()
	runtime, err := New(context.Background(), Config{ServiceName: "test-service", SampleRatio: ratio}, WithSpanExporter(exporter), WithMetricReader(reader))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return runtime, exporter, reader
}

func TestIdentityCorrelationAndBoundedMetrics(t *testing.T) {
	runtime, exporter, reader := testRuntime(t, 1)
	ctx, finish := runtime.StartOperation(context.Background(), "definitions.draft.save", Identity{ActorID: "actor-1", DraftID: "draft-7"})
	ctx = WithIdentity(ctx, Identity{DefinitionID: "definition-9", DraftRevision: "2"})
	finish(nil)
	finish(errors.New("second completion"))
	if err := runtime.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Status.Code != codes.Ok {
		t.Fatalf("spans: %#v", spans)
	}
	attributes := map[string]string{}
	for _, a := range spans[0].Attributes {
		attributes[string(a.Key)] = a.Value.AsString()
	}
	for key, value := range map[string]string{"smartfactory.actor_id": "actor-1", "smartfactory.draft_id": "draft-7", "smartfactory.definition_id": "definition-9", "smartfactory.draft_revision": "2"} {
		if attributes[key] != value {
			t.Errorf("%s = %q", key, attributes[key])
		}
	}
	logs := LogAttrs(ctx)
	if len(logs) != 6 {
		t.Fatalf("log attributes lost identity or trace: %#v", logs)
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	var completed int64
	var durations uint64
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch points := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, point := range points.DataPoints {
					completed += point.Value
					if point.Attributes.Len() != 2 {
						t.Errorf("metric identity labels: %#v", point.Attributes)
					}
				}
			case metricdata.Histogram[float64]:
				for _, point := range points.DataPoints {
					durations += point.Count
					if point.Attributes.Len() != 2 {
						t.Errorf("metric identity labels: %#v", point.Attributes)
					}
				}
			}
		}
	}
	if completed != 1 || durations != 1 {
		t.Fatalf("completion count=%d duration count=%d", completed, durations)
	}
}

func TestHTTPPropagationPreservesRemoteParent(t *testing.T) {
	upstream, _, _ := testRuntime(t, 1)
	ctx, finish := upstream.StartOperation(context.Background(), "command.dispatch", Identity{CommandID: "command-1"})
	header := http.Header{}
	InjectHTTP(ctx, header)
	remote := ExtractHTTP(context.Background(), header)
	if !trace.SpanContextFromContext(remote).IsRemote() {
		t.Fatal("HTTP parent was not marked remote")
	}
	downstream, exporter, _ := testRuntime(t, 0)
	child, childFinish := downstream.StartOperation(remote, "command.execute", Identity{CommandID: "command-1"})
	childFinish(nil)
	finish(nil)
	if trace.SpanContextFromContext(child).TraceID() != trace.SpanContextFromContext(ctx).TraceID() {
		t.Fatal("distributed trace ID changed")
	}
	if err := downstream.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Parent.SpanID() != trace.SpanContextFromContext(ctx).SpanID() {
		t.Fatalf("remote parent sampling: %#v", spans)
	}
}

func TestFailureDoesNotExportErrorBody(t *testing.T) {
	runtime, exporter, _ := testRuntime(t, 1)
	_, finish := runtime.StartOperation(context.Background(), "definitions.draft.publish", Identity{MessageID: "message-1"})
	finish(errors.New("password=private-payload"))
	if err := runtime.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Status.Code != codes.Error {
		t.Fatalf("failure status: %#v", spans)
	}
	if len(spans[0].Events) != 1 {
		t.Fatal("missing exception event")
	}
	for _, a := range spans[0].Events[0].Attributes {
		if strings.Contains(a.Value.AsString(), "private-payload") {
			t.Fatal("business error body was exported")
		}
	}
}

func TestOTLPHTTPExportUsesSignalPaths(t *testing.T) {
	var mu sync.Mutex
	paths := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) == 0 {
			t.Error("OTLP request body missing")
		}
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	runtime, err := New(context.Background(), Config{ServiceName: "test-otlp", SampleRatio: 1, TraceExporter: "otlp", MetricExporter: "otlp", Endpoint: server.URL + "/collector", ExportTimeout: time.Second, MetricInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	_, finish := runtime.StartOperation(context.Background(), "definitions.draft.validate", Identity{DraftID: "draft-1"})
	finish(nil)
	if err := runtime.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if paths["/collector/v1/traces"] == 0 || paths["/collector/v1/metrics"] == 0 {
		t.Fatalf("OTLP paths: %#v", paths)
	}
}

func TestEnvironmentAndSamplerRules(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "traceidratio")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.25")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "1200")
	c, err := ConfigFromEnv("sf-test")
	if err != nil {
		t.Fatal(err)
	}
	if c.SampleRatio != 0.25 || c.ExportTimeout != 1200*time.Millisecond || c.Sampler != "traceidratio" {
		t.Fatalf("config: %#v", c)
	}
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "NaN")
	if _, err := ConfigFromEnv("sf-test"); err == nil {
		t.Fatal("NaN sampler was accepted")
	}
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1")
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://user:secret@localhost:4318")
	if _, err := ConfigFromEnv("sf-test"); err == nil {
		t.Fatal("credential URL was accepted")
	}
}

func TestDisabledSDKAndShutdown(t *testing.T) {
	runtime, err := New(context.Background(), Config{ServiceName: "disabled", Disabled: true, TraceExporter: "otlp"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, finish := runtime.StartOperation(context.Background(), "command.execute", Identity{CommandID: "command-1"})
	finish(nil)
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatal("disabled SDK created a trace")
	}
	if len(LogAttrs(ctx)) != 1 {
		t.Fatal("disabled SDK lost business identity")
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPContextRequestIDsRejectionsAndLocalParent(t *testing.T) {
	runtime, exporter, _ := testRuntime(t, 1)
	previousTrace, previousMetric := otel.GetTracerProvider(), otel.GetMeterProvider()
	runtime.Install()
	t.Cleanup(func() { otel.SetTracerProvider(previousTrace); otel.SetMeterProvider(previousMetric) })
	for _, value := range []string{"", "invalid value", strings.Repeat("x", 129), "valid-request-1"} {
		request := httptest.NewRequest("POST", "http://example.test/denied", nil)
		request.Header.Set("X-Request-ID", value)
		response := httptest.NewRecorder()
		HTTPContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := w.(http.Flusher); !ok {
				t.Error("Flusher interface lost")
			}
			id := r.Header.Get("X-Request-ID")
			if id == "" || !requestIDPattern.MatchString(id) {
				t.Error("invalid canonical request ID")
			}
			if value == "valid-request-1" && id != value {
				t.Error("valid request ID changed")
			}
			before := trace.SpanContextFromContext(r.Context())
			header := http.Header{"Traceparent": []string{"00-11111111111111111111111111111111-2222222222222222-01"}}
			after := trace.SpanContextFromContext(ExtractHTTP(r.Context(), header))
			if before.SpanID() != after.SpanID() {
				t.Error("repeat extraction replaced local HTTP span")
			}
			w.WriteHeader(http.StatusForbidden)
		})).ServeHTTP(response, request)
		if !requestIDPattern.MatchString(response.Header().Get("X-Request-ID")) {
			t.Error("response request ID missing")
		}
	}
	if err := runtime.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 4 {
		t.Fatalf("HTTP spans: %d", len(spans))
	}
	for _, span := range spans {
		if span.Name != "http.request" || span.Status.Code != codes.Error {
			t.Errorf("HTTP rejection span: %#v", span)
		}
	}
}

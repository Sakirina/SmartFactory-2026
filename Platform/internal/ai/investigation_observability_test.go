package ai_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/observability"
	"go.opentelemetry.io/otel"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestInvestigationActualHTTPOTLPIdentity(t *testing.T) {
	var mu sync.Mutex
	var spans []*tracepb.Span
	var exports []*collectortrace.ExportTraceServiceRequest
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/v1/traces" {
			value := &collectortrace.ExportTraceServiceRequest{}
			if err := proto.Unmarshal(raw, value); err != nil {
				t.Error(err)
			}
			mu.Lock()
			exports = append(exports, value)
			for _, resource := range value.ResourceSpans {
				for _, scope := range resource.ScopeSpans {
					spans = append(spans, scope.Spans...)
				}
			}
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer collector.Close()
	previousTraces, previousMetrics, previousPropagator := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	runtime, err := observability.New(context.Background(), observability.Config{ServiceName: "ai-evidence-test", NodeID: "edge-a", TraceExporter: "otlp", MetricExporter: "none", Endpoint: collector.URL, Sampler: "always_on", ExportTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Install()
	defer func() {
		_ = runtime.Shutdown(context.Background())
		otel.SetTracerProvider(previousTraces)
		otel.SetMeterProvider(previousMetrics)
		otel.SetTextMapPropagator(previousPropagator)
	}()
	h := evidenceFixture(t, false, "responses", true)
	result, _ := h.investigate(t, "调查温度、执行与历史依据")
	if result.EvidenceStatus != "supported" {
		t.Fatal(result.EvidenceStatus)
	}
	if err := runtime.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	operations := map[string]int{}
	calls := map[string]bool{}
	for _, span := range spans {
		if hex.EncodeToString(span.TraceId) != "11111111111111111111111111111111" {
			continue
		}
		operations[span.Name]++
		if span.Name != "ai.investigation.evidence" {
			continue
		}
		attrs := map[string]string{}
		for _, a := range span.Attributes {
			attrs[a.Key] = a.Value.GetStringValue()
		}
		if attrs["ai.investigation_id"] != result.ID || attrs["ai.evidence_id"] == "" || attrs["ai.tool_call_id"] == "" || attrs["ai.tool_name"] == "" {
			t.Fatal("OTLP lost investigation identity", attrs)
		}
		calls[attrs["ai.tool_call_id"]] = true
		transaction := false
		for _, child := range spans {
			if child.Name == "store.transaction" && bytes.Equal(child.ParentSpanId, span.SpanId) {
				transaction = true
			}
		}
		if !transaction {
			t.Fatal("evidence transaction has no operation parent")
		}
	}
	for _, operation := range []string{"http.request", "ai.investigation.begin", "ai.investigation.evidence", "ai.investigation.complete", "store.transaction"} {
		if operations[operation] == 0 {
			t.Fatal("OTLP missing operation", operation)
		}
	}
	if len(calls) != 5 {
		t.Fatal("OTLP lost tool loops", calls)
	}
	if directory := os.Getenv("SF_AI_EVIDENCE_ARTIFACT_DIR"); directory != "" {
		for i, value := range exports {
			raw, err := protojson.MarshalOptions{Indent: "  "}.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(directory, fmtOTLP(i)), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("actual OTLP HTTP exports=%d, investigation calls=%d, operations=%v", len(exports), len(calls), operations)
}

func fmtOTLP(i int) string { return "otlp-investigation-" + strconv.Itoa(i) + ".json" }

func TestInvestigationActualHTTPDisconnectPersistsCancellation(t *testing.T) {
	h := evidenceFixture(t, false, "responses", true)
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "POST", h.url+"/api/sf/v1/assistant", strings.NewReader(`{"messages":[{"role":"user","content":"模拟超时"}]}`))
	r.Header.Set("Authorization", "Bearer "+h.fixture.Token)
	r.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	id := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var event map[string]any
			_ = json.Unmarshal([]byte(line[6:]), &event)
			id, _ = event["investigation_id"].(string)
			break
		}
	}
	cancel()
	_ = response.Body.Close()
	if id == "" {
		t.Fatal("disconnect lost investigation identity")
	}
	for i := 0; i < 100; i++ {
		value, err := h.server.Store.Investigation(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if value.Status == "cancelled" {
			if value.EvidenceStatus != "unsupported" || value.Answer != "" {
				t.Fatal("cancelled investigation content", value)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("disconnected request left running investigation")
}

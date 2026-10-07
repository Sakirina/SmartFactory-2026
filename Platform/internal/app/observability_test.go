package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/pkg/model"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestApplicationHTTPDraftAndTransactionTrace(t *testing.T) {
	var mu sync.Mutex
	var spans []*tracepb.Span
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/v1/traces" {
			var exported collectortrace.ExportTraceServiceRequest
			if err := proto.Unmarshal(data, &exported); err != nil {
				t.Error(err)
			}
			mu.Lock()
			for _, resource := range exported.ResourceSpans {
				for _, scope := range resource.ScopeSpans {
					spans = append(spans, scope.Spans...)
				}
			}
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_METRICS_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_always_on")
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "1000")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	directory := t.TempDir()
	a, err := Open(context.Background(), Options{Mode: "cloud", NodeID: "trace-test", Address: address, DSN: filepath.Join(directory, "trace.db"), KeyFile: filepath.Join(directory, "trace.key"), BootstrapPassword: "trace-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, entity := range []model.Entity{{ID: "factory", Kind: "asset"}, {ID: "device", Kind: "device", ParentID: "factory"}} {
		if _, err := a.Store.Put(context.Background(), "entity", entity.ID, 0, entity); err != nil {
			t.Fatal(err)
		}
	}
	token, _, err := a.Server.Identity.Login(context.Background(), "admin", "trace-test-password", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	client := &http.Client{Timeout: 3 * time.Second}
	base := "http://" + address
	for attempt := 0; attempt < 100; attempt++ {
		response, err := client.Get(base + "/health")
		if err == nil {
			response.Body.Close()
			break
		}
		if attempt == 99 {
			t.Fatal("application HTTP listener unavailable", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	draft := model.Draft{ID: "trace-draft", Definition: model.Definition{ID: "trace-definition", Name: "Trace definition", Kind: "analysis", SchemaVersion: model.ContractVersion, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "input", Type: "input"}}, Outputs: []model.Output{{NodeID: "input", Key: "reading", Type: "number"}}}}
	request := func(path string, body any) {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		output, _ := io.ReadAll(response.Body)
		if response.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, response.StatusCode, output)
		}
		if response.Header.Get("X-Request-ID") == "" {
			t.Fatal("HTTP response request ID missing")
		}
	}
	request("/api/sf/v1/drafts", application.SaveDraftInput{Draft: draft})
	request("/api/sf/v1/drafts/trace-draft/validate", map[string]any{"expected_version": 1})
	request("/api/sf/v1/drafts/trace-draft/publish", map[string]any{"expected_version": 1})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("application did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	var publication *tracepb.Span
	operations := map[string]bool{}
	for _, span := range spans {
		if hex.EncodeToString(span.TraceId) != "11111111111111111111111111111111" {
			continue
		}
		operations[span.Name] = true
		if span.Name == "definitions.draft.publish" {
			publication = span
		}
	}
	for _, name := range []string{"http.request", "definitions.draft.save", "definitions.draft.validate", "definitions.draft.publish", "store.transaction"} {
		if !operations[name] {
			t.Errorf("missing operation %s", name)
		}
	}
	if publication == nil {
		t.Fatal("publication span missing")
	}
	var transaction *tracepb.Span
	for _, span := range spans {
		if span.Name == "store.transaction" && bytes.Equal(span.ParentSpanId, publication.SpanId) {
			transaction = span
			break
		}
	}
	if transaction == nil {
		t.Fatal("publication did not own its database transaction span")
	}
	attributes := map[string]string{}
	for _, a := range transaction.Attributes {
		attributes[a.Key] = a.Value.GetStringValue()
	}
	if attributes["smartfactory.actor_id"] != "admin" || attributes["smartfactory.draft_id"] != "trace-draft" || attributes["smartfactory.definition_id"] != "trace-definition" || attributes["smartfactory.request_id"] == "" {
		t.Fatalf("transaction identity: %#v", attributes)
	}
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTypedDraftRoutesRunReviewedPublicationAndPreserveJSONNumbers(t *testing.T) {
	s, token := scopedServer(t, false)
	draft := model.Draft{ID: "working-copy", Definition: model.Definition{ID: "published-rule", Name: "Rule", Kind: "analysis", SchemaVersion: model.ContractVersion, GroupID: "a", Selector: model.Selector{DeviceIDs: []string{"device-a"}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "input", Type: "input", Params: map[string]any{"threshold": json.Number("9007199254740993")}}}}}
	w := call(s, token, http.MethodPost, "/api/sf/v1/drafts", map[string]any{"draft": draft, "expected_version": 0})
	if w.Code != http.StatusOK {
		t.Fatalf("typed save: %d %s", w.Code, w.Body.String())
	}
	doc, err := s.Store.Get(context.Background(), "draft", draft.ID)
	saved, decodeErr := store.Decode[model.Draft](doc)
	if err != nil || decodeErr != nil || saved.Definition.Nodes[0].Params["threshold"].(json.Number).String() != "9007199254740993" {
		t.Fatalf("numeric precision: %+v %v %v", saved, err, decodeErr)
	}
	for _, operation := range []string{"validate", "publish"} {
		w = call(s, token, http.MethodPost, "/api/sf/v1/drafts/working-copy/"+operation, map[string]int64{"expected_version": 0})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("stale %s: %d %s", operation, w.Code, w.Body.String())
		}
	}
	w = call(s, token, http.MethodPost, "/api/sf/v1/drafts/working-copy/validate", map[string]int64{"expected_version": 1})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"valid":true`) {
		t.Fatalf("typed validate: %d %s", w.Code, w.Body.String())
	}
	w = call(s, token, http.MethodPost, "/api/sf/v1/drafts/working-copy/publish", map[string]int64{"expected_version": 1})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"published-rule"`) {
		t.Fatalf("typed publish: %d %s", w.Code, w.Body.String())
	}
}

func TestTypedDraftRoutesAllowIncompleteGraphsAndRejectMalformedBodies(t *testing.T) {
	s, token := scopedServer(t, false)
	w := call(s, token, http.MethodPost, "/api/sf/v1/drafts", map[string]any{"draft": map[string]any{"id": "unfinished", "definition": map[string]any{"id": "unfinished", "group_id": "a", "selector": map[string]any{"device_ids": []string{"device-a"}}}}})
	if w.Code != http.StatusOK {
		t.Fatalf("unfinished graph save: %d %s", w.Code, w.Body.String())
	}
	w = call(s, token, http.MethodPost, "/api/sf/v1/drafts/unfinished/validate", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"valid":false`) {
		t.Fatalf("unfinished graph validation: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"expected_version":-1}`, `{"unknown":true}`, `{"expected_version":"one"}`, `{} {}`} {
		r := httptest.NewRequest(http.MethodPost, "/api/sf/v1/drafts/unfinished/publish", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code < 400 || w.Code >= 500 || !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatalf("body %s: %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestTypedDraftRoutesPropagateRemoteTraceAndBusinessIdentity(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	defer func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) }()
	s, token := scopedServer(t, false)
	body := []byte(`{"draft":{"id":"trace-draft","definition":{"id":"trace-definition","group_id":"a","selector":{"device_ids":["device-a"]}}},"expected_version":0}`)
	r := httptest.NewRequest(http.MethodPost, "/api/sf/v1/drafts", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	r.Header.Set("X-Request-ID", "trace-request")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("traced save: %d %s", w.Code, w.Body.String())
	}
	spans := []sdktrace.ReadOnlySpan{}
	for _, span := range recorder.Ended() {
		if span.Name() == "definitions.draft.save" {
			spans = append(spans, span)
		}
	}
	if len(spans) != 1 || spans[0].SpanContext().TraceID().String() != "0123456789abcdef0123456789abcdef" || spans[0].Parent().SpanID().String() != "0123456789abcdef" {
		t.Fatalf("trace propagation: %+v", spans)
	}
	attributes := map[string]string{}
	for _, attribute := range spans[0].Attributes() {
		attributes[string(attribute.Key)] = attribute.Value.AsString()
	}
	for key, expected := range map[string]string{"smartfactory.request_id": "trace-request", "smartfactory.actor_id": "user", "smartfactory.draft_id": "trace-draft", "smartfactory.definition_id": "trace-definition", "smartfactory.draft_revision": "1"} {
		if attributes[key] != expected {
			t.Errorf("%s=%q, expected %q", key, attributes[key], expected)
		}
	}
}

func TestTypedCompatibilityEndpointReportsAppliedMigrations(t *testing.T) {
	s, token := scopedServer(t, false)
	w := call(s, token, http.MethodGet, "/api/sf/v1/compatibility", nil)
	var result CompatibilityStatus
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Driver != "sqlite" || int64(len(result.Migrations)) != result.Manifest.Databases["sqlite"].MigrationMaximum || result.Manifest.ContractVersion != model.ContractVersion {
		t.Fatalf("compatibility: %d %s", w.Code, w.Body.String())
	}
	w = call(s, token, http.MethodGet, "/api/sf/v1/contracts/operations", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"x-contract-source":"typed-registration"`) {
		t.Fatalf("registered contract: %d %s", w.Code, w.Body.String())
	}
}

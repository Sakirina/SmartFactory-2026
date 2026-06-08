package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"competition2026/product/datatransfer/internal/config"
	dtruntime "competition2026/product/datatransfer/internal/runtime"
)

func TestManagementEndpoints(t *testing.T) {
	rt := dtruntime.New(config.Defaults())
	handler := Handler(rt)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d", health.Code)
	}

	notReady := httptest.NewRecorder()
	handler.ServeHTTP(notReady, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if notReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before grpc serving = %d", notReady.Code)
	}

	rt.SetGRPCServing(true)
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("/readyz after grpc serving = %d", ready.Code)
	}

	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "datatransfer_ready 1") {
		t.Fatalf("metrics body missing ready metric:\n%s", metrics.Body.String())
	}
}

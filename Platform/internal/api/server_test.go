package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStaticAndAIHandlersCanCoexist(t *testing.T) {
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) })
	s := &Server{StaticDir: t.TempDir(), MCP: mcp, Chat: mcp}
	h := s.Handler()
	for _, path := range []string{"/mcp/read", "/api/sf/v1/assistant"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 202 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != 405 {
		t.Fatalf("static mutation: %d", w.Code)
	}
}

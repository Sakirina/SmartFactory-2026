package nodeidentity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestRemoteAuthorityRevalidatesOriginalCredentialAndBoundsTimeout(t *testing.T) {
	token := strings.Repeat("a", 64)
	w := model.WorkloadIdentity{ID: "workload", NodeID: "edge", Program: "edge", Purpose: "runtime", Enabled: true, Generation: 3, Version: 4, InstanceID: "instance", InstanceEpoch: 2}
	var delayed, revoked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-SF-Instance-ID") != "instance" {
			t.Error("original authenticated credential or instance was not forwarded")
			http.Error(wr, "denied", 401)
			return
		}
		if delayed.Load() {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
			return
		}
		if revoked.Load() {
			http.Error(wr, "revoked", 401)
			return
		}
		json.NewEncoder(wr).Encode(w)
	}))
	defer server.Close()
	service := &Service{AuthorityURL: server.URL, HTTPClient: server.Client()}
	p, err := service.Authenticate(context.Background(), token, "instance")
	if err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	if _, err = service.ValidateTx(&store.Tx{Ctx: context.Background()}, p, "config.read"); !errors.Is(err, identity.ErrAuthentication) {
		t.Fatal("current revoked remote subject was accepted", err)
	}
	revoked.Store(false)
	delayed.Store(true)
	start := time.Now()
	if _, err = service.ValidateTx(&store.Tx{Ctx: context.Background()}, p, "credential.resolve"); !errors.Is(err, ErrAuthorityUnavailable) {
		t.Fatal("unavailable authority did not stop new credential authorization", err)
	}
	elapsed := time.Since(start)
	if elapsed < 2800*time.Millisecond || elapsed > 4500*time.Millisecond {
		t.Fatal("authority timeout was not bounded", elapsed)
	}
	t.Log("original token and current revision revalidated; revoked subject rejected; unavailable authority fails closed within 3 seconds")
}

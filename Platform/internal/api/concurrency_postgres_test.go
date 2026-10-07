package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func TestPostgresDeadlockReturnsHTTP409WithoutRepeatingCallback(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "http_deadlock")
	s, err := store.Open(context.Background(), dsn, "http-deadlock", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.DB.SetMaxOpenConns(3)
	t.Cleanup(func() { s.Close() })
	manager := &identity.Manager{Store: s, Master: make([]byte, 32)}
	if _, err = manager.CreateUser(context.Background(), model.Actor{}, model.User{ID: "operator", Login: "operator", Name: "Operator", Roles: []string{"engineer"}, Resources: []string{"*"}, Active: true}, "test-password-1234", "", 0); err != nil {
		t.Fatal(err)
	}
	token, _, err := manager.Login(context.Background(), "operator", "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err = s.Put(context.Background(), "counter", id, 0, map[string]int{"value": 0}); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{Store: s, Identity: manager}
	mux := http.NewServeMux()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	failures := make(chan error, 2)
	var calls atomic.Int64
	server.route(mux, "POST /deadlock/{first}", "read", func(w http.ResponseWriter, r *http.Request, _ identity.Principal) error {
		first, second := r.PathValue("first"), "a"
		if first == "a" {
			second = "b"
		}
		err := s.Write(r.Context(), func(tx *store.Tx) error {
			calls.Add(1)
			if _, err := tx.Get("counter", first); err != nil {
				return err
			}
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return r.Context().Err()
			}
			if _, err := tx.Get("counter", second); err != nil {
				return err
			}
			return tx.SetEphemeral("counter", first, map[string]int{"value": 1})
		})
		failures <- err
		if err == nil {
			w.WriteHeader(200)
		}
		return err
	})
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	statuses := make(chan int, 2)
	for _, first := range []string{"a", "b"} {
		go func() {
			request, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/deadlock/"+first, nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response, err := httpServer.Client().Do(request)
			if err != nil {
				statuses <- 0
				return
			}
			response.Body.Close()
			statuses <- response.StatusCode
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(4 * time.Second):
			close(release)
			t.Fatal("both HTTP business callbacks did not enter")
		}
	}
	close(release)
	counts := map[int]int{}
	for i := 0; i < 2; i++ {
		select {
		case code := <-statuses:
			counts[code]++
		case <-time.After(5 * time.Second):
			t.Fatal("HTTP deadlock response timeout")
		}
	}
	if counts[200] != 1 || counts[409] != 1 || calls.Load() != 2 {
		t.Fatal(counts, calls.Load())
	}
	retryable := 0
	for i := 0; i < 2; i++ {
		if err := <-failures; errors.Is(err, store.ErrRetryable) && errors.Is(err, store.ErrConflict) {
			retryable++
		}
	}
	if retryable != 1 {
		t.Fatal("recognizable deadlock failures", retryable)
	}
	t.Log("actual PostgreSQL deadlock across two HTTP requests: status200=1 status409=1, two business callbacks executed once each")
}

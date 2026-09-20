package configcenter

import (
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestFailedApplicationRetainsEffectiveValue(t *testing.T) {
	ctx := context.Background()
	makeService := func(name string) *Service {
		db, err := store.Open(ctx, filepath.Join(t.TempDir(), name+".db"), name, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return &Service{Store: db, Identity: &identity.Manager{Store: db, Master: make([]byte, 32)}}
	}
	remote, local := makeService("config"), makeService("edge")
	p := Parameter{ID: "control.start_ttl_ms", Program: "edge", Schema: map[string]any{"type": "integer", "minimum": 1000}, Value: 10000, Dynamic: true}
	if _, err := remote.Put(ctx, model.Actor{}, p, 0); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	remote.RegisterInternal(mux, "test-only")
	server := httptest.NewServer(mux)
	defer server.Close()
	subscriber := Subscriber{URL: server.URL, Token: "test-only", NodeID: "edge", Local: local, OnApply: local.ApplyPolicy}
	params, err := remote.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = subscriber.Apply(ctx, params); err != nil {
		t.Fatal(err)
	}
	p.Value = 2000
	if _, err = remote.Put(ctx, model.Actor{}, p, 1); err != nil {
		t.Fatal(err)
	}
	subscriber.OnApply = func(context.Context) error { return errors.New("consumer rejected update") }
	params, err = remote.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = subscriber.Apply(ctx, params); err != nil {
		t.Fatal(err)
	}
	value, err := local.Value(ctx, p.ID)
	if err != nil || fmt.Sprint(value) != "10000" || local.Store.Policy().StartTTLMS != 10000 {
		t.Fatal("failed value replaced effective configuration", value, err)
	}
	states, err := remote.List(ctx)
	if err != nil || states[0].Applications["edge"].State != "failed" || states[0].Effective["edge"] != 1 {
		t.Fatal(states, err)
	}
	items, err := remote.Store.Deliveries(ctx, "config_update", 100)
	if err != nil || len(items) != 1 {
		t.Fatal("pending update lost", len(items), err)
	}
	subscriber.OnApply = local.ApplyPolicy
	if err = subscriber.Apply(ctx, params); err != nil {
		t.Fatal(err)
	}
	if local.Store.Policy().StartTTLMS != 2000 {
		t.Fatal("failed version did not retry")
	}
	params[0].Value = 9000
	if err = subscriber.Apply(ctx, params); err != nil {
		t.Fatal(err)
	}
	if local.Store.Policy().StartTTLMS != 2000 {
		t.Fatal("same-version conflict replaced effective value")
	}
	// Restarting the subscriber retains the immutable received-content digest.
	restarted := Subscriber{URL: server.URL, Token: "test-only", NodeID: "edge", Local: local, OnApply: local.ApplyPolicy}
	if err = restarted.Apply(ctx, params); err != nil {
		t.Fatal(err)
	}
	if local.Store.Policy().StartTTLMS != 2000 {
		t.Fatal("same-version conflict accepted after restart")
	}
}

func TestIndependentSubscriptionAppliesDynamicAndDefersStatic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	makeService := func(name string) *Service {
		db, e := store.Open(ctx, filepath.Join(t.TempDir(), name+".db"), name, make([]byte, 32))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { db.Close() })
		return &Service{Store: db, Identity: &identity.Manager{Store: db, Master: make([]byte, 32)}}
	}
	remote, local := makeService("config"), makeService("cloud")
	parameter := Parameter{ID: "control.start_ttl_ms", Program: "cloud", Dynamic: true, Schema: map[string]any{"type": "integer", "minimum": 1000}, Value: 10000}
	static := Parameter{ID: "static", Program: "cloud", Schema: map[string]any{"type": "integer"}, Value: 1}
	for _, p := range []Parameter{parameter, static} {
		if _, e := remote.Put(ctx, model.Actor{}, p, 0); e != nil {
			t.Fatal(e)
		}
	}
	mux := http.NewServeMux()
	remote.RegisterInternal(mux, "service-token")
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	subscriber := &Subscriber{URL: httpServer.URL, Token: "service-token", NodeID: "cloud", Local: local, OnApply: local.ApplyPolicy}
	done := make(chan error, 1)
	go func() { done <- subscriber.Run(ctx) }()
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("configuration state did not converge")
	}
	wait(func() bool {
		values, _ := remote.List(ctx)
		return len(values) == 2 && values[0].Effective["cloud"] == 1 && values[1].Effective["cloud"] == 1
	})
	parameter.Value = 2000
	static.Value = 2
	for _, p := range []Parameter{parameter, static} {
		if _, e := remote.Put(ctx, model.Actor{}, p, 1); e != nil {
			t.Fatal(e)
		}
	}
	wait(func() bool {
		values, _ := remote.List(ctx)
		return len(values) == 2 && values[0].Effective["cloud"] == 2 && values[1].State == "restart_required"
	})
	if local.Store.Policy().StartTTLMS != 2000 {
		t.Fatal("dynamic value did not reach running control policy")
	}
	value, e := local.Value(ctx, "static")
	if e != nil || value.(interface{ String() string }).String() != "1" {
		t.Fatalf("static applied without restart: %v %v", value, e)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not stop")
	}
	// A new program instance applies the pending static version on startup.
	restarted := &Subscriber{URL: httpServer.URL, Token: "service-token", NodeID: "cloud", Local: local}
	snapshot, e := remote.snapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.Apply(context.Background(), snapshot); e != nil {
		t.Fatal(e)
	}
	value, e = local.Value(context.Background(), "static")
	if e != nil || value.(interface{ String() string }).String() != "2" {
		t.Fatalf("restart did not apply static: %v %v", value, e)
	}
}

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/api"
	"competition2026/product/platform/internal/businessfixture"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/thingsboard"
	"competition2026/product/platform/pkg/model"
)

func TestBusinessNativeWorkerImmediatePeriodicAndFailureRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, e := store.Open(ctx, filepath.Join(t.TempDir(), "worker.db"), "edge-a", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	f, e := businessfixture.Seed(ctx, db, make([]byte, 32), "business-worker-fixture")
	if e != nil {
		t.Fatal(e)
	}
	detail, e := f.Business.AlarmDetail(ctx, f.Principal, f.AlarmID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Put(ctx, "tb_alarm_mapping", f.AlarmID, 0, map[string]any{"native": map[string]any{"id": map[string]any{"id": "worker-native"}}}); e != nil {
		t.Fatal(e)
	}
	var recoverNative atomic.Bool
	attempts := make(chan time.Time, 8)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			fmt.Fprint(w, `{"token":"fixture"}`)
			return
		}
		attempts <- time.Now()
		if !recoverNative.Load() {
			http.Error(w, `{"message":"temporarily unavailable"}`, 503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": map[string]any{"id": "worker-native"}, "startTs": detail.Alarm.StartedMS, "acknowledged": true, "ackTs": detail.Alarm.StartedMS + 10})
	}))
	defer httpServer.Close()
	a := &Application{Store: db, Server: &api.Server{Store: db, Identity: f.Identity, Engine: f.Engine, Mode: "cloud", NodeID: "edge-a"}, Native: &thingsboard.Adapter{Store: db, Client: &thingsboard.Client{URL: httpServer.URL}}}
	started := time.Now()
	a.startBusinessWorkers(ctx)
	defer func() { cancel(); a.wg.Wait() }()
	var first, second time.Time
	select {
	case first = <-attempts:
		if first.Sub(started) > time.Second {
			t.Fatal("worker did not run immediately")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no immediate worker attempt")
	}
	deadline := time.Now().Add(time.Second)
	for {
		d, e := db.Get(ctx, "native_alarm_sync", "edge-a")
		if e == nil {
			status, _ := store.Decode[model.NativeAlarmSyncStatus](d)
			if status.Status == "retrying" && status.FailedMappings == 1 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not persist initial failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	recoverNative.Store(true)
	select {
	case second = <-attempts:
		if second.Sub(first) < 4900*time.Millisecond {
			t.Fatal("worker ignored configured interval", second.Sub(first))
		}
	case <-time.After(7 * time.Second):
		t.Fatal("periodic worker did not retry")
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		d, e := f.Business.AlarmDetail(ctx, f.Principal, f.AlarmID)
		if e == nil && d.Native != nil && d.Native.Acknowledged && d.Alarm.Acknowledged && d.NativeSync.Status == "current" && d.NativeSync.FailedMappings == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker recovery did not update business application", d, e)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("formal startBusinessWorkers entry ran immediately, persisted HTTP503, retried after %s and applied native acknowledgement with current sync status", second.Sub(first))
}

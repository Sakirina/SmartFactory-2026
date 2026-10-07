package thingsboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestBusinessNativePollFairnessRotationAndRecovery(t *testing.T) {
	ctx := context.Background()
	db, e := store.Open(ctx, filepath.Join(t.TempDir(), "poll.db"), "edge-a", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var recovered atomic.Bool
	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/auth/login" {
			fmt.Fprint(w, `{"token":"fixture"}`)
			return
		}
		reads.Add(1)
		id := strings.TrimPrefix(r.URL.Path, "/api/alarm/")
		if id == "native-001" && !recovered.Load() {
			http.Error(w, `{"message":"deleted fixture alarm"}`, 404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": map[string]any{"id": id}, "startTs": 1, "acknowledged": true, "ackTs": 2})
	}))
	defer server.Close()
	for i := 0; i < 66; i++ {
		id := fmt.Sprintf("alarm-%03d", i)
		native := map[string]any{"id": map[string]any{"id": fmt.Sprintf("native-%03d", i)}}
		if i == 0 {
			native = map[string]any{"id": "damaged"}
		}
		if _, e = db.Put(ctx, "tb_alarm_mapping", id, 0, map[string]any{"native": native}); e != nil {
			t.Fatal(e)
		}
	}
	a := &Adapter{Store: db, Client: &Client{URL: server.URL, Username: "fixture", Password: "fixture"}}
	applied := map[string]int{}
	apply := func(_ context.Context, id string, u model.NativeAlarmUpdate) (model.NativeAlarmState, error) {
		if id == "alarm-002" && !recovered.Load() {
			return model.NativeAlarmState{}, errors.New("controlled application failure")
		}
		applied[id]++
		return model.NativeAlarmState{ID: id, NativeAlarmUpdate: u}, nil
	}
	status := func() model.NativeAlarmSyncStatus {
		doc, e := db.Get(ctx, "native_alarm_sync", "edge-a")
		if e != nil {
			t.Fatal(e)
		}
		s, e := store.Decode[model.NativeAlarmSyncStatus](doc)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	if e = a.PollAlarmStates(ctx, apply); e == nil {
		t.Fatal("failed mappings not reported")
	}
	s := status()
	if len(applied) != 61 || reads.Load() != 63 || s.Cursor != "alarm-063" || s.FailedMappings != 3 || s.Status != "retrying" {
		t.Fatal("first bounded round", len(applied), reads.Load(), s)
	}
	if e = a.PollAlarmStates(ctx, apply); e != nil {
		t.Fatal(e)
	}
	s = status()
	if len(applied) != 63 || s.Cursor != "" || s.FailedMappings != 3 || s.Status != "retrying" {
		t.Fatal("later mappings blocked or failure discarded", len(applied), s)
	}
	recovered.Store(true)
	if _, e = db.Put(ctx, "tb_alarm_mapping", "alarm-000", 1, map[string]any{"native": map[string]any{"id": map[string]any{"id": "native-000"}}}); e != nil {
		t.Fatal(e)
	}
	if e = a.PollAlarmStates(ctx, apply); e != nil {
		t.Fatal(e)
	}
	s = status()
	if len(applied) != 66 || s.FailedMappings != 0 || s.Status != "current" || s.Cursor != "alarm-063" {
		t.Fatal("repaired mappings not revisited", len(applied), s)
	}
	if e = a.PollAlarmStates(ctx, apply); e != nil {
		t.Fatal(e)
	}
	if s = status(); s.Cursor != "" || s.BatchLimit != 64 || s.TimeoutMS != 10000 {
		t.Fatal(s)
	}
	t.Log("66 mappings rotate in bounded pages; damaged mapping, persistent HTTP404 and application failure preserve retries while later alarms synchronize; all recover after repair")
}

func TestBusinessNativePollDeadlineResumesAfterSlowMapping(t *testing.T) {
	ctx := context.Background()
	db, e := store.Open(ctx, filepath.Join(t.TempDir(), "deadline.db"), "edge-a", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			fmt.Fprint(w, `{"token":"fixture"}`)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/alarm/")
		if id == "slow" {
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": map[string]any{"id": id}, "startTs": 1})
	}))
	defer server.Close()
	for _, v := range []struct{ id, native string }{{"first", "slow"}, {"second", "healthy"}} {
		if _, e = db.Put(ctx, "tb_alarm_mapping", v.id, 0, map[string]any{"native": map[string]any{"id": map[string]any{"id": v.native}}}); e != nil {
			t.Fatal(e)
		}
	}
	a := &Adapter{Store: db, Client: &Client{URL: server.URL}}
	applied := 0
	apply := func(_ context.Context, id string, u model.NativeAlarmUpdate) (model.NativeAlarmState, error) {
		applied++
		return model.NativeAlarmState{ID: id, NativeAlarmUpdate: u}, nil
	}
	started := time.Now()
	if e = a.PollAlarmStates(ctx, apply); e == nil {
		t.Fatal("slow mapping ignored the round deadline")
	}
	elapsed := time.Since(started)
	if elapsed < 9*time.Second || elapsed > 20*time.Second || applied != 0 {
		t.Fatal(elapsed, applied)
	}
	doc, e := db.Get(ctx, "native_alarm_sync", "edge-a")
	if e != nil {
		t.Fatal(e)
	}
	status, _ := store.Decode[model.NativeAlarmSyncStatus](doc)
	if status.Cursor != "first" || status.FailedMappings != 1 {
		t.Fatal(status)
	}
	if e = a.PollAlarmStates(ctx, apply); e != nil || applied != 1 {
		t.Fatal("healthy mapping after timed-out entry was blocked", applied, e)
	}
	t.Logf("round deadline stopped slow HTTP after %s and persisted cursor for the next healthy mapping", elapsed)
}

package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type testConfig struct{}

func (testConfig) Value(context.Context, string) (any, error) {
	return map[string]any{"token": "plugin-test-only"}, nil
}
func pluginFixture(t *testing.T) *Manager {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "plugins.db"), "cloud", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &Manager{Store: s, Config: testConfig{}}
}
func TestActualOrganizationProcessPullAndCrashIsolation(t *testing.T) {
	binary := os.Getenv("SF_HR_PLUGIN_BINARY")
	if binary == "" {
		t.Skip("set SF_HR_PLUGIN_BINARY to the built example process")
	}
	m := pluginFixture(t)
	ctx := context.Background()
	now := m.Store.Now()
	m.Store.Now = func() time.Time { return now }
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer plugin-test-only" {
			t.Error("process credential missing")
		}
		batch := OrganizationSync{ID: "hr-1", Sequence: 1, Full: true, Departments: []Department{{ID: "operations", Name: "Operations"}}, Members: []Member{{ID: "operator", Login: "operator", Name: "Operator", DepartmentID: "operations", Active: true}}}
		if r.URL.Query().Get("after") == "1" {
			batch.ID = "hr-2"
			batch.Sequence = 2
			batch.Full = false
			batch.Members[0].Active = false
		}
		_ = json.NewEncoder(w).Encode(batch)
	}))
	defer feed.Close()
	spec := Spec{ID: "hr", Name: "HR", Kind: "organization", Executable: binary, PollMS: 1000, Enabled: true, CredentialID: "hr.credential", Config: map[string]any{"endpoint": feed.URL}}
	if _, err := m.Put(ctx, model.Actor{}, spec, 0); err != nil {
		t.Fatal(err)
	}
	failed := spec
	failed.ID = "failed"
	failed.Executable = "/usr/bin/false"
	if _, err := m.Put(ctx, model.Actor{}, failed, 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Poll(ctx); err == nil {
		t.Fatal("process crash not reported")
	}
	doc, err := m.Store.Get(ctx, "user", "operator")
	if err != nil {
		t.Fatal("other plugin stopped after process crash", err)
	}
	u, err := store.Decode[model.User](doc)
	if err != nil || !u.Active || u.DepartmentID != "operations" {
		t.Fatal(u, err)
	}
	u.Roles = []string{"engineer"}
	u.Resources = []string{"factory"}
	if _, err = m.Store.Put(ctx, "user", u.ID, doc.Version, u); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	_ = m.Poll(ctx)
	doc, err = m.Store.Get(ctx, "user", "operator")
	if err != nil {
		t.Fatal(err)
	}
	u, err = store.Decode[model.User](doc)
	if err != nil || u.Active || len(u.Roles) != 1 || u.Roles[0] != "engineer" {
		t.Fatal("incremental sync overwrote platform grants or failed to disable user", u, err)
	}
	stateDoc, _ := m.Store.Get(ctx, "plugin_status", "hr")
	state, _ := store.Decode[Status](stateDoc)
	if state.Sequence != 2 || state.State != "healthy" {
		t.Fatal(state)
	}
}
func TestPushCommitsBeforeAcknowledgementAndRejectsCredentialAndSequence(t *testing.T) {
	m := pluginFixture(t)
	ctx := context.Background()
	spec := Spec{ID: "hr", Name: "HR", Kind: "organization", Executable: "/usr/bin/false", PollMS: 60000, Enabled: true, PushCredentialID: "hr.push"}
	if _, err := m.Put(ctx, model.Actor{}, spec, 0); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/plugins/{id}/organization", m.Push)
	batch := OrganizationSync{ID: "first", Source: "hr", Sequence: 1, Members: []Member{{ID: "person", Name: "Person", Login: "person", Active: true}}}
	call := func(token string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(batch)
		r := httptest.NewRequest("POST", "/internal/plugins/hr/organization", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := call("wrong"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for i := 0; i < 2; i++ {
		if w := call("plugin-test-only"); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	doc, err := m.Store.Get(ctx, "user", "person")
	if err != nil || doc.Version != 1 {
		t.Fatal("duplicate organization operation", doc.Version, err)
	}
	batch.ID = "second"
	batch.Sequence = 2
	batch.Members[0].Name = "Changed"
	if _, err = m.Store.DB.Exec(`CREATE TRIGGER write_failure BEFORE UPDATE ON documents WHEN NEW.kind='user' BEGIN SELECT RAISE(ABORT,'injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if w := call("plugin-test-only"); w.Code == 200 {
		t.Fatal("failed write acknowledged")
	}
	if _, err = m.Store.Get(ctx, "event", "second"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	doc, err = m.Store.Get(ctx, "user", "person")
	u, _ := store.Decode[model.User](doc)
	if err != nil || u.Name != "Person" {
		t.Fatal("failed import was not atomic", u, err)
	}
}

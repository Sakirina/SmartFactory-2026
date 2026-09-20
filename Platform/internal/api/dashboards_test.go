package api

import (
	"context"
	"encoding/json"
	"testing"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestDashboardPermissionsVersionsAndReplication(t *testing.T) {
	s, token := scopedServer(t, false)
	d := model.Dashboard{ID: "main", Title: "Production", GroupID: "a", DeviceIDs: []string{"device-a"}, Keys: []string{"temperature"}, Metrics: []model.DashboardMetric{{DeviceID: "device-a", Key: "temperature", Label: "Temperature"}}, WindowMS: 3600000, RefreshMS: 1000, ShowAlarms: true, ShowSources: true}
	request := func(expected int64) any { return map[string]any{"dashboard": d, "expected_version": expected} }
	w := call(s, token, "POST", "/api/sf/v1/dashboards", request(0))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call(s, token, "POST", "/api/sf/v1/dashboards", request(0)); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	d.Metrics[0].DeviceID = "device-b"
	if w = call(s, token, "POST", "/api/sf/v1/dashboards", request(1)); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	d.Metrics[0].DeviceID = "device-a"
	d.ID = "restricted"
	d.GroupID = "b"
	if _, e := s.Store.Put(context.Background(), "dashboard", d.ID, 0, d); e != nil {
		t.Fatal(e)
	}
	w = call(s, token, "GET", "/api/sf/v1/dashboards", nil)
	var dashboards []model.Dashboard
	if e := json.Unmarshal(w.Body.Bytes(), &dashboards); e != nil || len(dashboards) != 1 || dashboards[0].Version != 1 {
		t.Fatal(w.Body.String(), e)
	}
	changes, e := s.Store.Changes(context.Background(), 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, change := range changes {
		if change.Document.Kind == "dashboard" && change.Document.ID == "main" {
			found = true
		}
	}
	if !found {
		t.Fatal("saved dashboard was not queued for cloud-to-edge synchronization")
	}
	userDoc, _ := s.Store.Get(context.Background(), "user", "user")
	u, _ := store.Decode[model.User](userDoc)
	u.AI = true
	u.Roles = []string{"ai"}
	if _, e = s.Identity.CreateUser(context.Background(), model.Actor{}, u, "", "", u.Version); e != nil {
		t.Fatal(e)
	}
	if w = call(s, token, "POST", "/api/sf/v1/dashboards", request(0)); w.Code != 403 {
		t.Fatal("AI dashboard mutation", w.Code, w.Body.String())
	}
	if e = s.Identity.Permit(context.Background(), identity.Principal{User: u}, "dashboard", ""); e == nil {
		t.Fatal("AI gained dashboard permission")
	}
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestTaskRoutesCheckResourcesRolesVersionsAndActualState(t *testing.T) {
	s, token := scopedServer(t, false)
	for _, device := range []string{"device-a", "device-b"} {
		job := model.Job{ID: "backfill:" + device, Kind: "recompute", Status: "pending", DeviceID: device, FromMS: 1, ToMS: 2}
		if _, err := s.Store.Put(context.Background(), "job", job.ID, 0, job); err != nil {
			t.Fatal(err)
		}
	}
	id := "recompute:backfill:device-a:1"
	path := "/api/sf/v1/tasks/" + url.PathEscape(id)
	w := call(s, token, http.MethodGet, "/api/sf/v1/tasks?limit=100", nil)
	var list application.TaskList
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 1 || list.Items[0].ID != id {
		t.Fatalf("scoped list %d %s", w.Code, w.Body.String())
	}
	task := list.Items[0]
	if !slices.Contains(task.AllowedActions, "cancel") {
		t.Fatal(task)
	}
	if w := call(s, token, http.MethodGet, "/api/sf/v1/tasks/"+url.PathEscape("recompute:backfill:device-b:1"), nil); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(s, token, http.MethodGet, "/api/sf/v1/task-queues", nil); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(s, token, http.MethodPost, path+"/cancel", model.TaskAction{ExpectedVersion: "stale"}); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(s, token, http.MethodPost, path+"/cancel", model.TaskAction{ExpectedVersion: task.Version})
	var cancelled model.Task
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cancelled) != nil || cancelled.State != "cancelled" || cancelled.BusinessState != "cancelled" || !slices.Contains(cancelled.AllowedActions, "retry") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(s, token, http.MethodPost, path+"/retry", model.TaskAction{ExpectedVersion: task.Version}); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(s, token, http.MethodPost, path+"/retry", model.TaskAction{ExpectedVersion: cancelled.Version})
	var retried model.Task
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &retried) != nil || retried.ID != task.ID || retried.RiverID != task.RiverID || retried.State != "available" {
		t.Fatal(w.Code, w.Body.String())
	}
	doc, err := s.Store.Get(context.Background(), "user", "user")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.Decode[model.User](doc)
	if err != nil {
		t.Fatal(err)
	}
	user.Roles = []string{"viewer"}
	if _, err = s.Store.Put(context.Background(), "user", "user", doc.Version, user); err != nil {
		t.Fatal(err)
	}
	if w := call(s, token, http.MethodPost, path+"/cancel", model.TaskAction{ExpectedVersion: retried.Version}); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Log("HTTP task list/details filter all resources; cancel/retry require role and exact version; same business/River identity survives retry")
}

func TestAITaskReaderCannotCancelOrRetry(t *testing.T) {
	s, token := scopedServer(t, true)
	if err := s.Store.Write(context.Background(), func(tx *store.Tx) error {
		return tx.Enqueue("ai-denial", "tb_entity", "device-a", model.Entity{ID: "device-a"})
	}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"cancel", "retry"} {
		w := call(s, token, http.MethodPost, "/api/sf/v1/tasks/tb_entity:ai-denial/"+action, model.TaskAction{ExpectedVersion: "irrelevant"})
		if w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestRecomputeTaskResolvesDefinitionResourceScope(t *testing.T) {
	s, token := scopedServer(t, false)
	ctx := context.Background()
	for _, target := range []struct{ id, device string }{{"allowed-rule", "device-a"}, {"cross-resource-rule", "device-b"}} {
		definition := model.Definition{ID: target.id, GroupID: "a", Selector: model.Selector{DeviceIDs: []string{target.device}}}
		if _, err := s.Store.Put(ctx, "definition", definition.ID, 0, definition); err != nil {
			t.Fatal(err)
		}
		job := model.Job{ID: target.id, Kind: "recompute", Status: "pending", DeviceID: "device-a", DefinitionID: target.id, FromMS: 1, ToMS: 2}
		if _, err := s.Store.Put(ctx, "job", job.ID, 0, job); err != nil {
			t.Fatal(err)
		}
		w := call(s, token, http.MethodGet, "/api/sf/v1/tasks/"+url.PathEscape("recompute:"+target.id+":1"), nil)
		want := 200
		if target.device == "device-b" {
			want = 403
		}
		if w.Code != want {
			t.Fatal(target, w.Code, w.Body.String())
		}
	}
}

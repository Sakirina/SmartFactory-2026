package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/thingsboard"
	"competition2026/product/platform/pkg/model"
)

func TestBusinessNativeCEAlarmLifecycle(t *testing.T) {
	base, credentials := os.Getenv("SF_NATIVE_URL"), os.Getenv("SF_NATIVE_CREDENTIALS")
	if base == "" || credentials == "" {
		t.Skip("isolated native CE URL and credential file required")
	}
	raw, e := os.ReadFile(credentials)
	if e != nil {
		t.Fatal(e)
	}
	var auth struct{ Username, Password string }
	if e = json.Unmarshal(raw, &auth); e != nil {
		t.Fatal(e)
	}
	s, f := businessServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	a := &thingsboard.Adapter{Store: s.Store, Client: &thingsboard.Client{URL: base, Username: auth.Username, Password: auth.Password}}
	before, e := s.BusinessApplication().AlarmDetail(ctx, f.Principal, f.AlarmID)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Alarm(ctx, before.Alarm); e != nil {
		t.Fatal(e)
	}
	mapping, e := s.Store.Get(ctx, "tb_alarm_mapping", f.AlarmID)
	if e != nil {
		t.Fatal(e)
	}
	var bound struct{ Native map[string]any }
	if e = store.DecodeJSON(mapping.Data, &bound); e != nil {
		t.Fatal(e)
	}
	initial, e := thingsboard.NativeAlarmUpdate("thingsboard:edge-a", bound.Native)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cleanup, c := context.WithTimeout(context.Background(), 20*time.Second)
		defer c()
		if e := a.Client.Do(cleanup, "DELETE", "/api/alarm/"+initial.NativeID, nil, nil); e != nil {
			t.Errorf("native alarm cleanup: %v", e)
		}
		doc, e := s.Store.Get(cleanup, "tb_mapping", before.Alarm.EntityID)
		if e == nil {
			m, e := store.Decode[thingsboard.Mapping](doc)
			if e == nil {
				if e = a.Client.Do(cleanup, "DELETE", "/api/device/"+m.Native.ID, nil, nil); e != nil {
					t.Errorf("native device cleanup: %v", e)
				}
			}
		}
	})
	query := func() model.QueryRow {
		t.Helper()
		w := call(s, f.Token, "POST", "/api/sf/v1/queries/alarms", model.QueryRequest{Limit: 10})
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var page model.QueryPage
		if e := json.Unmarshal(w.Body.Bytes(), &page); e != nil || len(page.Items) != 1 {
			t.Fatal(page, e)
		}
		return page.Items[0]
	}
	first := query()
	detail := before
	for _, input := range []application.AlarmActionInput{{RequestID: "native-assignment", Action: "assign", AssigneeID: f.Principal.User.ID, Reason: "原生回传前分配责任人"}, {RequestID: "native-note", Action: "note", Reason: "原生回传前保存现场检查备注"}} {
		input.ExpectedVersion = detail.Case.Version
		input.ExpectedActionVersion = detail.ActionVersion
		w := call(s, f.Token, "POST", "/api/sf/v1/alarms/"+f.AlarmID+"/actions", input)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if e = json.Unmarshal(w.Body.Bytes(), &detail); e != nil {
			t.Fatal(e)
		}
	}
	handled := query()
	if handled.Revision == first.Revision || !strings.Contains(string(handled.Data), `"assignee_id":"operator-a"`) {
		t.Fatal("manual handling query projection", string(handled.Data))
	}
	for _, action := range []string{"ack", "clear"} {
		if e = a.Client.Do(ctx, "POST", "/api/alarm/"+initial.NativeID+"/"+action, nil, nil); e != nil {
			t.Fatal(e)
		}
	}
	if e = a.PollAlarmStates(ctx, s.BusinessApplication().UpdateNativeAlarm); e != nil {
		t.Fatal(e)
	}
	detail, e = s.BusinessApplication().AlarmDetail(ctx, f.Principal, f.AlarmID)
	if e != nil || detail.Native == nil || !detail.Native.Acknowledged || !detail.Native.Cleared || !detail.Alarm.Acknowledged || !detail.Alarm.Active || detail.Case.AssigneeID != f.Principal.User.ID || len(detail.Case.Operations) != 2 || detail.NativeSync.Status != "current" {
		t.Fatal(detail, e)
	}
	native := query()
	if native.Revision == handled.Revision || !strings.Contains(string(native.Data), `"native"`) || !strings.Contains(string(native.Data), `"cleared":true`) {
		t.Fatal("native state query projection", string(native.Data))
	}
	if e = f.Observe(ctx, 36, time.Now().UnixMilli()); e != nil {
		t.Fatal(e)
	}
	if e = a.Alarm(ctx, before.Alarm); e != nil {
		t.Fatal(e)
	}
	var current map[string]any
	if e = a.Client.Do(ctx, "GET", "/api/alarm/"+initial.NativeID, nil, &current); e != nil {
		t.Fatal(e)
	}
	now, e := thingsboard.NativeAlarmUpdate(initial.SourceID, current)
	if e != nil || !now.Cleared || !now.Acknowledged {
		t.Fatal(now, e)
	}
	if e = a.PollAlarmStates(ctx, s.BusinessApplication().UpdateNativeAlarm); e != nil {
		t.Fatal(e)
	}
	newer, e := s.BusinessApplication().AlarmDetail(ctx, f.Principal, f.AlarmID)
	if e != nil || newer.Case.Version != detail.Case.Version || newer.Native.Version != detail.Native.Version {
		t.Fatal("duplicate native state changed manual/native journal", newer, e)
	}
	if _, e = s.BusinessApplication().UpdateNativeAlarm(ctx, f.AlarmID, initial); e != nil {
		t.Fatal("old native replay", e)
	}
	conflict := now
	conflict.NativeID = "unbound-native-id"
	if _, e = s.BusinessApplication().UpdateNativeAlarm(ctx, f.AlarmID, conflict); e == nil {
		t.Fatal("unbound callback accepted")
	}
	changed := now
	changed.ClearedMS++
	if _, e = s.BusinessApplication().UpdateNativeAlarm(ctx, f.AlarmID, changed); e == nil || errors.Is(e, store.ErrNotFound) {
		t.Fatal("forged source version accepted", e)
	}
	t.Logf("actual CE ack/clear -> bounded polling -> business application -> HTTP query revision %s/%s/%s; delayed projection keeps native lifecycle; local rule remains active; responsibility and notes retained", first.Revision, handled.Revision, native.Revision)
}

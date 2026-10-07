package api

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/businessfixture"
	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func businessServer(t *testing.T) (*Server, *businessfixture.Fixture) {
	t.Helper()
	ctx := context.Background()
	db, e := store.Open(ctx, filepath.Join(t.TempDir(), "business-http.db"), "edge-a", make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	f, e := businessfixture.Seed(ctx, db, make([]byte, 32), "business-http-password-2026")
	if e != nil {
		t.Fatal(e)
	}
	return &Server{Store: db, Identity: f.Identity, Engine: f.Engine, Mode: "cloud", NodeID: "edge-a", ServiceToken: "fixture-native-service-token"}, f
}

func TestBusinessHTTPContinuousAlarmReviewAndNativeCallback(t *testing.T) {
	s, f := businessServer(t)
	ctx := context.Background()
	w := call(s, f.Token, "GET", "/api/sf/v1/alarms/"+f.AlarmID, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var reviewed model.AlarmDetail
	if e := json.Unmarshal(w.Body.Bytes(), &reviewed); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		until := time.NewTimer(2100 * time.Millisecond)
		defer until.Stop()
		for {
			select {
			case now := <-ticker.C:
				if e := f.Observe(ctx, 35, now.UnixMilli()); e != nil {
					done <- e
					return
				}
			case <-until.C:
				done <- nil
				return
			}
		}
	}()
	time.Sleep(2 * time.Second)
	in := application.AlarmActionInput{RequestID: "http-continuous-ack", ExpectedVersion: reviewed.Case.Version, ExpectedActionVersion: reviewed.ActionVersion, Action: "acknowledge", Reason: "详情读取后持续采样两秒，再确认当前告警"}
	w = call(s, f.Token, "POST", "/api/sf/v1/alarms/"+f.AlarmID+"/actions", in)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if w.Code != 200 {
		t.Fatal("single reviewed HTTP confirmation under continuous observation", w.Code, w.Body.String())
	}
	var updated model.AlarmDetail
	if e := json.Unmarshal(w.Body.Bytes(), &updated); e != nil || !updated.Alarm.Acknowledged || updated.Alarm.Count <= reviewed.Alarm.Count {
		t.Fatal(updated, e)
	}
	for _, input := range []application.AlarmActionInput{{RequestID: "http-assign", Action: "assign", AssigneeID: f.Principal.User.ID, Reason: "现场责任分配"}, {RequestID: "http-note", Action: "note", Reason: "原生告警回传前的人工备注"}} {
		input.ExpectedVersion = updated.Case.Version
		input.ExpectedActionVersion = updated.ActionVersion
		w = call(s, f.Token, "POST", "/api/sf/v1/alarms/"+f.AlarmID+"/actions", input)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if e := json.Unmarshal(w.Body.Bytes(), &updated); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Store.Put(ctx, "tb_alarm_mapping", f.AlarmID, 0, map[string]any{"native": map[string]any{"id": map[string]any{"id": "native-bound", "entityType": "ALARM"}}, "version": updated.Alarm.Version}); e != nil {
		t.Fatal(e)
	}
	native := model.NativeAlarmUpdate{SourceID: "thingsboard:edge-a", NativeID: "native-bound", StartedMS: updated.Alarm.StartedMS, Acknowledged: true, AcknowledgedMS: time.Now().UnixMilli(), Cleared: true, ClearedMS: time.Now().UnixMilli()}
	native.SourceVersion = model.NativeAlarmVersion(native)
	w = call(s, "wrong-token", "POST", "/internal/native/alarms/"+f.AlarmID, native)
	if w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(s, s.ServiceToken, "POST", "/internal/native/alarms/"+f.AlarmID, native)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(s, s.ServiceToken, "POST", "/internal/native/alarms/"+f.AlarmID, native)
	if w.Code != 200 {
		t.Fatal("native duplicate", w.Code, w.Body.String())
	}
	w = call(s, f.Token, "GET", "/api/sf/v1/alarms/"+f.AlarmID, nil)
	var final model.AlarmDetail
	if e := json.Unmarshal(w.Body.Bytes(), &final); e != nil || w.Code != 200 || final.Native == nil || !final.Native.Cleared || !final.Alarm.Active || final.Case.AssigneeID != f.Principal.User.ID || len(final.Case.Operations) != 3 {
		t.Fatal(w.Code, w.Body.String(), e)
	}
	t.Logf("one HTTP read then 2s continuous Engine.Process, one confirmation HTTP200, count %d -> %d; authenticated native callback, replay and manual responsibility/note retention verified", reviewed.Alarm.Count, final.Alarm.Count)
}

func TestBusinessHTTPProtocolTemplatesSemanticAndHandover(t *testing.T) {
	s, f := businessServer(t)
	w := call(s, f.Token, "GET", "/api/sf/v1/device-protocols", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var protocols []deviceconfig.ProtocolMetadata
	if e := json.Unmarshal(w.Body.Bytes(), &protocols); e != nil || len(protocols) != 3 {
		t.Fatal(protocols, e)
	}
	w = call(s, f.Token, "POST", "/api/sf/v1/device-configurations/validate", deviceconfig.Request{Protocol: "modbus_tcp", Parameters: &deviceconfig.Parameters{Kind: "connector", ConnectorID: "invalid", Connection: map[string]any{"host": "127.0.0.1", "port": 65536}}})
	var validated deviceconfig.Result
	if e := json.Unmarshal(w.Body.Bytes(), &validated); e != nil || w.Code != 200 || validated.Valid || len(validated.Issues) == 0 {
		t.Fatal(w.Code, w.Body.String(), e)
	}
	w = call(s, f.Token, "POST", "/api/sf/v1/template-batches", model.TemplateBatchInput{ID: "http-five", RequestID: "http-five", GroupID: "factory", Instances: f.Instances})
	var batch model.TemplateBatch
	if e := json.Unmarshal(w.Body.Bytes(), &batch); e != nil || w.Code != 200 || batch.Status != "prepared" || len(batch.Drafts) != 25 {
		t.Fatal(w.Code, w.Body.String(), e)
	}
	w = call(s, f.Token, "POST", "/api/sf/v1/drafts/"+f.DraftID+"/semantic-diff", application.SemanticInput{ExpectedVersion: 2, PublishedVersion: 1})
	var diff model.SemanticDifference
	if e := json.Unmarshal(w.Body.Bytes(), &diff); e != nil || w.Code != 200 || !diff.Equivalent {
		t.Fatal(w.Code, w.Body.String(), e)
	}
	w = call(s, f.Token, "POST", "/api/sf/v1/drafts/"+f.DraftID+"/impact", application.ImpactInput{ExpectedVersion: 2, Budget: 512})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(s, f.Token, "POST", "/api/sf/v1/work-orders", application.CreateWorkOrderInput{ID: "http-work", RequestID: "http-work", Title: "交班工作项", GroupID: "factory", AssigneeID: f.Principal.User.ID, AlarmIDs: []string{f.AlarmID}, Reason: "HTTP 创建工单"})
	var work model.WorkOrderDetail
	if e := json.Unmarshal(w.Body.Bytes(), &work); e != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), e)
	}
	w = call(s, f.Token, "POST", "/api/sf/v1/work-orders/http-work/handovers", application.HandoverInput{RequestID: "http-handover", ExpectedVersion: work.WorkOrder.Version, FromUserID: f.Principal.User.ID, ToUserID: f.OtherPrincipal.User.ID, PendingItems: []string{"继续观察告警变化"}, Evidence: []model.HandoverEvidence{{Kind: "work_order", ID: work.WorkOrder.ID, Version: work.WorkOrder.Version}}, Reason: "HTTP 交班"})
	if e := json.Unmarshal(w.Body.Bytes(), &work); e != nil || w.Code != 200 || len(work.Handovers) != 1 || work.WorkOrder.AssigneeID != f.OtherPrincipal.User.ID {
		t.Fatal(w.Code, w.Body.String(), e)
	}
	w = call(s, f.Token, "POST", "/api/sf/v1/work-orders/http-work/actions", application.WorkOrderActionInput{RequestID: "http-stale", ExpectedVersion: 1, Action: "note", Reason: "过期版本"})
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(s, f.Token, "GET", "/api/sf/v1/work-orders", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Log("typed HTTP routes validated metadata, failed form validation, five-template batch, semantic compare, impact, work order handover and stale-version409")
}

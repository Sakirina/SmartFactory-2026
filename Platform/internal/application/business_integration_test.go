package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/businessfixture"
	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func businessFixture(t *testing.T, pg bool) *businessfixture.Fixture {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "business.db")
	if pg {
		dsn, _ = testdb.Postgres(t, "business")
	}
	s, err := store.Open(context.Background(), dsn, "edge-a", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.DB.SetMaxOpenConns(3)
	s.DB.SetMaxIdleConns(1)
	if s.Driver == "sqlite" {
		s.DB.SetMaxOpenConns(1)
	}
	t.Cleanup(func() { s.Close() })
	f, err := businessfixture.Seed(context.Background(), s, make([]byte, 32), "business-test-password-2026")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func alarmAction(t *testing.T, f *businessfixture.Fixture, action, id, assignee string) model.AlarmDetail {
	t.Helper()
	ctx := context.Background()
	d, err := f.Business.AlarmDetail(ctx, f.Principal, f.AlarmID)
	if err != nil {
		t.Fatal(err)
	}
	in := application.AlarmActionInput{RequestID: id, ExpectedVersion: d.Case.Version, ExpectedActionVersion: d.ActionVersion, Action: action, Reason: "现场检查记录", AssigneeID: assignee}
	d, err = f.Business.ActOnAlarm(ctx, f.Principal, f.AlarmID, in)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func checkBusinessLifecycle(t *testing.T, f *businessfixture.Fixture) {
	ctx := context.Background()
	before, err := f.Business.AlarmDetail(ctx, f.Principal, f.AlarmID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if err = f.Observe(ctx, 35, time.Now().UnixMilli()+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	input := application.AlarmActionInput{RequestID: "ack-review", ExpectedVersion: before.Case.Version, ExpectedActionVersion: before.ActionVersion, Action: "acknowledge", Reason: "已检查连续采样告警"}
	d, err := f.Business.ActOnAlarm(ctx, f.Principal, f.AlarmID, input)
	if err != nil || !d.Alarm.Acknowledged || !d.Case.Acknowledged || d.Alarm.Count <= before.Alarm.Count {
		t.Fatal("continuous observations should not invalidate manual review", d, err)
	}
	again, err := f.Business.ActOnAlarm(ctx, f.Principal, f.AlarmID, input)
	if err != nil || again.Case.Version != d.Case.Version {
		t.Fatal("idempotency", again, err)
	}
	input.Reason = "different request content"
	if _, err = f.Business.ActOnAlarm(ctx, f.Principal, f.AlarmID, input); !errors.Is(err, store.ErrConflict) {
		t.Fatal("id reused with changed content", err)
	}
	d = alarmAction(t, f, "assign", "assign-a", f.Principal.User.ID)
	d = alarmAction(t, f, "note", "note-a", "")
	if err = f.Observe(ctx, 25, time.Now().UnixMilli()+50); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Business.ActOnAlarm(ctx, f.Principal, f.AlarmID, application.AlarmActionInput{RequestID: "stale-lifecycle", ExpectedVersion: d.Case.Version, ExpectedActionVersion: d.ActionVersion, Action: "note", Reason: "must review recovery"}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("lifecycle revision must conflict", err)
	}
	d = alarmAction(t, f, "complete", "complete-a", "")
	if d.Case.Status != "completed" || d.Alarm.Active || len(d.Case.Operations) != 4 {
		t.Fatal(d)
	}
	work, err := f.Business.CreateWorkOrder(ctx, f.Principal, application.CreateWorkOrderInput{RequestID: "create-work", ID: "work-a", Title: "检查温度设备", Description: "复核控制设备与现场测量", GroupID: "factory", AssigneeID: f.Principal.User.ID, AlarmIDs: []string{f.AlarmID}, Reason: "需要继续跟踪"})
	if err != nil {
		t.Fatal(err)
	}
	work, err = f.Business.ChangeWorkOrder(ctx, f.Principal, work.WorkOrder.ID, application.WorkOrderActionInput{RequestID: "start-work", ExpectedVersion: work.WorkOrder.Version, Action: "start", Reason: "开始检查"})
	if err != nil {
		t.Fatal(err)
	}
	handover := application.HandoverInput{RequestID: "handover-work", ExpectedVersion: work.WorkOrder.Version, FromUserID: f.Principal.User.ID, ToUserID: f.OtherPrincipal.User.ID, PendingItems: []string{"复核次日温度曲线"}, Evidence: []model.HandoverEvidence{{Kind: "alarm", ID: f.AlarmID, Version: d.Alarm.Version, Description: "告警已恢复并完成处置"}}, Reason: "交班继续检查"}
	work, err = f.Business.HandoverWorkOrder(ctx, f.Principal, work.WorkOrder.ID, handover)
	if err != nil || len(work.Handovers) != 1 || work.WorkOrder.AssigneeID != f.OtherPrincipal.User.ID {
		t.Fatal(work, err)
	}
	replay, err := f.Business.HandoverWorkOrder(ctx, f.Principal, work.WorkOrder.ID, handover)
	if err != nil || replay.WorkOrder.Version != work.WorkOrder.Version || len(replay.Handovers) != 1 {
		t.Fatal("handover replay", replay, err)
	}
	if _, err = f.Business.ChangeWorkOrder(ctx, f.Principal, work.WorkOrder.ID, application.WorkOrderActionInput{RequestID: "old-owner-complete", ExpectedVersion: work.WorkOrder.Version, Action: "complete", Reason: "原责任人尝试完成"}); err == nil {
		t.Fatal("old assignee retained completion permission")
	}
	work, err = f.Business.ChangeWorkOrder(ctx, f.OtherPrincipal, work.WorkOrder.ID, application.WorkOrderActionInput{RequestID: "new-owner-complete", ExpectedVersion: work.WorkOrder.Version, Action: "complete", Reason: "承接人完成现场检查"})
	if err != nil || work.WorkOrder.Status != "completed" {
		t.Fatal(work, err)
	}
	if issues, e := f.Store.VerifyAudit(ctx); e != nil || len(issues) > 0 {
		t.Fatal(issues, e)
	}
	doc, err := f.Store.Get(ctx, "user", f.OtherPrincipal.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := store.Decode[model.User](doc)
	u.Active = false
	u.Version++
	if _, err = f.Store.Put(ctx, "user", u.ID, doc.Version, u); err != nil {
		t.Fatal(err)
	}
	work, err = f.Business.WorkOrderDetail(ctx, f.Principal, work.WorkOrder.ID)
	if err != nil || work.ResponsibilityIssue == "" {
		t.Fatal("inactive member must be visible as responsibility issue", work, err)
	}
	if _, err = f.Business.WorkOrderDetail(ctx, f.OtherPrincipal, work.WorkOrder.ID); err == nil {
		t.Fatal("revoked direct principal retained access")
	}
	t.Log("actual Engine.Process sampling, stable action review, lifecycle conflict, idempotent handling, assignee transfer, responsibility revocation and audit chain verified")
}
func TestBusinessWorkflowSQLite(t *testing.T)   { checkBusinessLifecycle(t, businessFixture(t, false)) }
func TestBusinessWorkflowPostgres(t *testing.T) { checkBusinessLifecycle(t, businessFixture(t, true)) }

func TestBusinessTemplateBatchesAndSemanticChanges(t *testing.T) {
	f := businessFixture(t, false)
	ctx := context.Background()
	batchInput := model.TemplateBatchInput{ID: "all-five", RequestID: "prepare-five", GroupID: "factory", Instances: f.Instances}
	batch, err := f.Business.PrepareTemplateBatch(ctx, f.Principal, batchInput)
	if err != nil || batch.Status != "prepared" || len(batch.Drafts) != 25 || len(batch.Bindings) != 5 {
		t.Fatalf("five template batch: %+v %v", batch, err)
	}
	for _, draft := range batch.Drafts {
		v, e := f.Business.Definitions.ValidateDraft(ctx, f.Principal, application.DraftInput{ID: draft.ID, ExpectedVersion: &draft.Version})
		if e != nil || !v.Valid {
			t.Fatalf("generated draft %s: %+v %v", draft.ID, v, e)
		}
	}
	again, err := f.Business.PrepareTemplateBatch(ctx, f.Principal, batchInput)
	if err != nil || again.Version != batch.Version || store.Hash(again) != store.Hash(batch) {
		t.Fatal("batch replay", err)
	}
	bad := batchInput
	bad.ID = "conflicting-batch"
	bad.RequestID = "conflicting-request"
	badBatch, err := f.Business.PrepareTemplateBatch(ctx, f.Principal, bad)
	if err != nil || badBatch.Status != "failed" || len(badBatch.Drafts) != 0 || len(badBatch.Failures) != 5 {
		t.Fatal("atomic conflict batch", badBatch, err)
	}
	retry := application.RetryTemplateBatchInput{RequestID: "retry-conflict", ExpectedVersion: badBatch.Version}
	failed, err := f.Business.RetryTemplateBatch(ctx, f.Principal, badBatch.ID, retry)
	if err != nil || failed.Version != 2 || len(failed.Attempts) != 2 {
		t.Fatal(failed, err)
	}
	if replay, e := f.Business.RetryTemplateBatch(ctx, f.Principal, badBatch.ID, retry); e != nil || replay.Version != 2 {
		t.Fatal("retry replay", replay, e)
	}
	baseDoc, _ := f.Store.Get(ctx, "draft", f.DraftID)
	base, _ := store.Decode[model.Draft](baseDoc)
	base.Definition.Nodes[0], base.Definition.Nodes[2] = base.Definition.Nodes[2], base.Definition.Nodes[0]
	base.Definition.Connections[0], base.Definition.Connections[1] = base.Definition.Connections[1], base.Definition.Connections[0]
	for i := range base.Definition.Connections {
		base.Definition.Connections[i].FromPort = "value"
		base.Definition.Connections[i].ToPort = "value"
	}
	base.Definition.Nodes[1].Params["high"] = json.Number("30.000")
	updated, err := f.Business.Definitions.SaveDraft(ctx, f.Principal, application.SaveDraftInput{Draft: base, ExpectedVersion: base.Version})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := f.Business.SemanticDifference(ctx, f.Principal, updated.ID, application.SemanticInput{ExpectedVersion: updated.Version, PublishedVersion: 1})
	if err != nil || !diff.Equivalent {
		t.Fatal("semantic reorder and exact numeric equivalence", diff, err)
	}
	updated.Definition.Nodes[1].Params["high"] = json.Number("9007199254740993")
	updated.Definition.Nodes[1].Params["extension"] = nil
	updated, err = f.Business.Definitions.SaveDraft(ctx, f.Principal, application.SaveDraftInput{Draft: updated, ExpectedVersion: updated.Version})
	if err != nil {
		t.Fatal(err)
	}
	diff, err = f.Business.SemanticDifference(ctx, f.Principal, updated.ID, application.SemanticInput{ExpectedVersion: updated.Version, PublishedVersion: 1})
	if err != nil || diff.Equivalent {
		t.Fatal(diff, err)
	}
	raw, _ := json.Marshal(diff)
	if !strings.Contains(string(raw), "9007199254740993") || !strings.Contains(string(raw), `"after_type":"null"`) {
		t.Fatal(string(raw))
	}
	updated.Definition.Dependencies = []string{"loop", "absent-rule"}
	updated.Version++
	if _, err = f.Store.Put(ctx, "draft", updated.ID, updated.Version-1, updated); err != nil {
		t.Fatal(err)
	}
	loop := model.Definition{ID: "loop", GroupID: "factory", Kind: "analysis", Status: "published", Version: 1, Dependencies: []string{updated.Definition.ID}}
	if _, err = f.Store.Put(ctx, "definition", loop.ID, 0, loop); err != nil {
		t.Fatal(err)
	}
	impact, err := f.Business.Impact(ctx, f.Principal, updated.ID, application.ImpactInput{ExpectedVersion: updated.Version, Budget: 100})
	if err != nil || impact.Complete {
		t.Fatal(impact, err)
	}
	codes := map[string]bool{}
	for _, issue := range impact.Issues {
		codes[issue.Code] = true
	}
	if !codes["cycle"] || !codes["missing_reference"] {
		t.Fatal(impact)
	}
	impact, err = f.Business.Impact(ctx, f.Principal, updated.ID, application.ImpactInput{ExpectedVersion: updated.Version, Budget: 1})
	if err != nil || impact.Complete || len(impact.References) > 1 {
		t.Fatal("bounded impact", impact, err)
	}
	t.Log("all 25 drafts from five templates validate; exact numeric values and null additions preserved; reordering equivalent; batch identity, failures, retries, cycles, missing references and budget verified")
}

type payloadRecorder struct {
	mu     sync.Mutex
	calls  []string
	before func()
}

func (r *payloadRecorder) ApplyConnectorConfiguration(_ context.Context, c model.ConnectorConfiguration, raw json.RawMessage) error {
	if r.before != nil {
		r.before()
	}
	parsed, e := deviceconfig.Validate(deviceconfig.Request{Protocol: c.Protocol, Config: raw})
	if e != nil {
		return e
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf("%d:%v", c.Version, parsed.Parameters.Connection["password"]))
	return nil
}

func TestBusinessConnectorSecretsAreFixedByVersion(t *testing.T) {
	f := businessFixture(t, false)
	ctx := context.Background()
	f.Business.Mode = "edge"
	input := application.SaveConnectorConfigurationInput{RequestID: "secret-v1", GroupID: "factory", EdgeID: "edge-a", Protocol: "mqtt_device", Parameters: deviceconfig.Parameters{Kind: "connector", ConnectorID: "secrets", Connection: map[string]any{"url": "tcp://127.0.0.1:1883", "password": "first-private-value"}, ReportStrategy: map[string]any{"mode": "ON_CHANGE_OR_REPORT_PERIOD", "period_seconds": 7, "deadband": 0}}}
	one, err := f.Business.SaveConnectorConfiguration(ctx, f.Principal, input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(one.Configuration.Config), "first-private-value") {
		t.Fatal("public configuration leaked password")
	}
	input.RequestID = "secret-v2"
	input.ExpectedVersion = 1
	input.Parameters.Connection["password"] = "second-private-value"
	recorder := &payloadRecorder{before: func() {
		if _, e := f.Business.SaveConnectorConfiguration(ctx, f.Principal, input); e != nil {
			t.Fatal(e)
		}
	}}
	if err = f.Business.ApplyConnectorConfiguration(ctx, one.Configuration, recorder); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 1 || recorder.calls[0] != "1:first-private-value" {
		t.Fatal("old task mixed new secret payload", recorder.calls)
	}
	two, err := f.Business.ConnectorConfiguration(ctx, f.Principal, one.Configuration.ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.Configuration.CredentialRef == two.Configuration.CredentialRef {
		t.Fatal("secret reference reused across versions")
	}
	recorder.before = nil
	if err = f.Business.ApplyConnectorConfiguration(ctx, two.Configuration, recorder); err != nil {
		t.Fatal(err)
	}
	if err = f.Business.ApplyConnectorConfiguration(ctx, one.Configuration, recorder); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 2 || recorder.calls[1] != "2:second-private-value" {
		t.Fatal("delayed old task was applied", recorder.calls)
	}
	status, err := f.Business.ConnectorConfiguration(ctx, f.Principal, one.Configuration.ID)
	if err != nil || status.Receipt.ConfigurationVersion != 2 || status.Receipt.Status != "applied" {
		t.Fatal(status, err)
	}
	parsed, err := deviceconfig.Validate(deviceconfig.Request{Protocol: two.Configuration.Protocol, Config: two.Configuration.Config})
	if err != nil || fmt.Sprint(parsed.Parameters.ReportStrategy["period_seconds"]) != "7" {
		t.Fatal("redaction lost report strategy", parsed, err)
	}
	changes, err := f.Store.Changes(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Document.Kind == "connector_configuration_secret" || strings.Contains(string(c.Document.Data), "first-private-value") || strings.Contains(string(c.Document.Data), "second-private-value") {
			t.Fatal("private payload entered synchronization")
		}
	}
	t.Log("version-bound encrypted payloads retain their original password and report strategy during concurrent save; delayed old task skips newer configuration; receipt version matches exact applied payload")
}

func TestBusinessWorkOrderChecksExecutionEvidenceAndRevokedMember(t *testing.T) {
	f := businessFixture(t, false)
	ctx := context.Background()
	execution := model.Execution{DownlinkID: "execution-evidence", DefinitionID: "fixture-alarm", DefinitionVersion: 1, Status: "succeeded", Version: 1}
	if _, err := f.Store.Put(ctx, "execution", execution.DownlinkID, 0, execution); err != nil {
		t.Fatal(err)
	}
	input := application.CreateWorkOrderInput{ID: "evidence-work", RequestID: "evidence-work", Title: "执行证据核对", GroupID: "factory", AssigneeID: f.Principal.User.ID, ExecutionIDs: []string{execution.DownlinkID}, Reason: "核对原始命令证据"}
	w, err := f.Business.CreateWorkOrder(ctx, f.Principal, input)
	if err != nil {
		t.Fatal(err)
	}
	evidence := model.CommandEvidence{ID: "private-evidence", ExecutionID: execution.DownlinkID, DeviceID: "private-device", CommandID: "private-command", ContentHash: "private", CollectedMS: time.Now().UnixMilli()}
	if err = f.Store.Write(ctx, func(tx *store.Tx) error { return tx.SaveControlEvidence(evidence) }); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Business.WorkOrderDetail(ctx, f.Principal, w.WorkOrder.ID); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("detail exposed inaccessible command evidence", err)
	}
	input.ID = "another-work"
	input.RequestID = "another-work"
	if _, err = f.Business.CreateWorkOrder(ctx, f.Principal, input); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("creation accepted inaccessible command evidence", err)
	}
	_, err = f.Business.HandoverWorkOrder(ctx, f.Principal, w.WorkOrder.ID, application.HandoverInput{RequestID: "evidence-handover", ExpectedVersion: w.WorkOrder.Version, FromUserID: f.Principal.User.ID, ToUserID: f.OtherPrincipal.User.ID, PendingItems: []string{"后续复核"}, Reason: "交班"})
	if !errors.Is(err, identity.ErrDenied) {
		t.Fatal("handover exposed inaccessible evidence", err)
	}
	t.Log("work order create/detail/handover all reject command evidence in an inaccessible asset")
}

type businessWriteHook struct {
	*store.Store
	before      func()
	fail        error
	failKind    string
	failID      string
	failVersion int64
}

func (h *businessWriteHook) Write(ctx context.Context, fn func(*store.Tx) error) error {
	if h.before != nil {
		f := h.before
		h.before = nil
		f()
	}
	return h.Store.Write(ctx, func(tx *store.Tx) error {
		if e := fn(tx); e != nil {
			return e
		}
		if h.fail != nil && h.failKind != "" {
			doc, e := tx.Get(h.failKind, h.failID)
			if errors.Is(e, store.ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			if doc.Version < h.failVersion {
				return nil
			}
		}
		return h.fail
	})
}

func TestBusinessWorkOrderRejectsEvidenceAddedAfterAuthorization(t *testing.T) {
	f := businessFixture(t, false)
	ctx := context.Background()
	x := model.Execution{DownlinkID: "concurrent-evidence", DefinitionID: "fixture-alarm", DefinitionVersion: 1, Status: "succeeded", Version: 1}
	if _, e := f.Store.Put(ctx, "execution", x.DownlinkID, 0, x); e != nil {
		t.Fatal(e)
	}
	f.Business.Store = &businessWriteHook{Store: f.Store, before: func() {
		e := f.Store.Write(ctx, func(tx *store.Tx) error {
			return tx.SaveControlEvidence(model.CommandEvidence{ID: "concurrent-private", ExecutionID: x.DownlinkID, DeviceID: "private-device", CommandID: "concurrent-private-command", ContentHash: "private", CollectedMS: time.Now().UnixMilli()})
		})
		if e != nil {
			t.Fatal(e)
		}
	}}
	_, err := f.Business.CreateWorkOrder(ctx, f.Principal, application.CreateWorkOrderInput{ID: "concurrent-work", RequestID: "concurrent-work", Title: "检查并发新增证据", GroupID: "factory", AssigneeID: f.Principal.User.ID, ExecutionIDs: []string{x.DownlinkID}, Reason: "读取权限后有新证据"})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatal("new evidence omitted from commit check", err)
	}
	if _, e := f.Store.Get(ctx, "work_order", "concurrent-work"); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("partial work order after conflict", e)
	}
	t.Log("execution evidence inserted after initial authorization causes transactional conflict and leaves no work order")
}

func checkBusinessConcurrentAlarm(t *testing.T, f *businessfixture.Fixture) {
	ctx := context.Background()
	d, e := f.Business.AlarmDetail(ctx, f.Principal, f.AlarmID)
	if e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			_, e := f.Business.ActOnAlarm(ctx, f.Principal, f.AlarmID, application.AlarmActionInput{RequestID: fmt.Sprintf("parallel-note-%d", i), ExpectedVersion: d.Case.Version, ExpectedActionVersion: d.ActionVersion, Action: "note", Reason: "并发现场备注"})
			results <- e
		}(i)
	}
	close(start)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			success++
		} else if errors.Is(e, store.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	current, e := f.Business.AlarmDetail(ctx, f.Principal, f.AlarmID)
	if e != nil || len(current.Case.Operations) != 1 || current.Case.Version != 1 {
		t.Fatal(current, e)
	}
	rollback := errors.New("business-atomicity-injected-failure")
	f.Business.Store = &businessWriteHook{Store: f.Store, fail: rollback, failKind: "alarm_case", failID: f.AlarmID, failVersion: current.Case.Version + 1}
	_, e = f.Business.ActOnAlarm(ctx, f.Principal, f.AlarmID, application.AlarmActionInput{RequestID: "rollback-note", ExpectedVersion: current.Case.Version, ExpectedActionVersion: current.ActionVersion, Action: "note", Reason: "验证原子回滚"})
	if !errors.Is(e, rollback) {
		t.Fatal(e)
	}
	f.Business.Store = f.Store
	after, e := f.Business.AlarmDetail(ctx, f.Principal, f.AlarmID)
	if e != nil || after.Case.Version != current.Case.Version {
		t.Fatal(after, e)
	}
	t.Log("two simultaneous reviewed actions commit exactly once; injected failure leaves case and operation journal unchanged")
}
func TestBusinessConcurrentAlarmSQLite(t *testing.T) {
	checkBusinessConcurrentAlarm(t, businessFixture(t, false))
}
func TestBusinessConcurrentAlarmPostgres(t *testing.T) {
	checkBusinessConcurrentAlarm(t, businessFixture(t, true))
}

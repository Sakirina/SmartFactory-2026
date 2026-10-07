package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestBusinessSemanticPermissionsAndDuplicateConnections(t *testing.T) {
	f := businessFixture(t, false)
	ctx := context.Background()
	doc, e := f.Store.Get(ctx, "draft", f.DraftID)
	if e != nil {
		t.Fatal(e)
	}
	d, _ := store.Decode[model.Draft](doc)
	d.Definition.Connections = append(d.Definition.Connections, d.Definition.Connections[0])
	d.Version++
	if _, e = f.Store.Put(ctx, "draft", d.ID, doc.Version, d); e != nil {
		t.Fatal(e)
	}
	diff, e := f.Business.SemanticDifference(ctx, f.Principal, d.ID, application.SemanticInput{ExpectedVersion: d.Version, PublishedVersion: 1})
	if e != nil || diff.Equivalent {
		t.Fatal("duplicate edge silently removed", diff, e)
	}
	hidden := model.Definition{ID: "hidden-secret-rule", GroupID: "private-factory", Kind: "analysis", Version: 1, Status: "published", Dependencies: []string{d.Definition.ID}}
	visible := model.Definition{ID: "visible-dependent-rule", GroupID: "factory", Kind: "analysis", Version: 1, Status: "published", Dependencies: []string{hidden.ID}}
	for _, v := range []model.Definition{hidden, visible} {
		if _, e = f.Store.Put(ctx, "definition", v.ID, 0, v); e != nil {
			t.Fatal(e)
		}
	}
	impact, e := f.Business.Impact(ctx, f.Principal, d.ID, application.ImpactInput{ExpectedVersion: d.Version, Budget: 100})
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range impact.References {
		if strings.Contains(r.ID, hidden.ID) || strings.Contains(r.Via, hidden.ID) || strings.Contains(r.Location, hidden.ID) {
			t.Fatal("restricted identity exposed", r)
		}
	}
	for _, issue := range impact.Issues {
		if strings.Contains(issue.ID, hidden.ID) || strings.Contains(issue.Location, hidden.ID) {
			t.Fatal("restricted issue identity exposed", issue)
		}
	}
	d.Definition.Dependencies = []string{hidden.ID}
	d.Version++
	if _, e = f.Store.Put(ctx, "draft", d.ID, d.Version-1, d); e != nil {
		t.Fatal(e)
	}
	if _, e = f.Business.SemanticDifference(ctx, f.Principal, d.ID, application.SemanticInput{ExpectedVersion: d.Version, PublishedVersion: 1}); !errors.Is(e, identity.ErrDenied) {
		t.Fatal("semantic diff exposed inaccessible dependency", e)
	}
	t.Log("duplicate connections remain semantically different; restricted transitive paths omit hidden identifiers; diff authorizes dependencies")
}

func TestBusinessTemplateValidationRecoveryAndPinnedInputs(t *testing.T) {
	f := businessFixture(t, false)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		edit func(*model.TemplateInstance)
	}{
		{"wrong-protocol", func(v *model.TemplateInstance) { v.DeviceID = "device-infrared-lighting" }},
		{"private-binding", func(v *model.TemplateInstance) { v.DeviceID = "private-device" }},
		{"stale-device", func(v *model.TemplateInstance) { v.DeviceVersion++ }},
		{"stale-connector", func(v *model.TemplateInstance) { v.ConfigurationVersion++ }},
		{"invalid-parameter", func(v *model.TemplateInstance) { v.Parameters = map[string]any{"temperature_high": -1000} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance := f.Instances[0]
			instance.ID = tc.name
			tc.edit(&instance)
			b, e := f.Business.PrepareTemplateBatch(ctx, f.Principal, model.TemplateBatchInput{ID: tc.name, RequestID: tc.name, GroupID: "factory", Instances: []model.TemplateInstance{instance}})
			if e != nil || b.Status != "failed" || len(b.Drafts) != 0 || len(b.Failures) != 1 {
				t.Fatal(b, e)
			}
		})
	}
	instance := f.Instances[4]
	instance.ID = "recover-counter"
	instance.Name = "可恢复计数"
	input := model.TemplateBatchInput{ID: "recover-batch", RequestID: "recover-batch", GroupID: "factory", Instances: []model.TemplateInstance{instance}}
	first, e := f.Business.PrepareTemplateBatch(ctx, f.Principal, input)
	if e != nil || first.Status != "prepared" {
		t.Fatal(first, e)
	}
	blocker := first.Drafts[0]
	input.ID = "recover-second"
	input.RequestID = "recover-second"
	input.Instances[0].ID = "recover-new-identity"
	failed, e := f.Business.PrepareTemplateBatch(ctx, f.Principal, input)
	if e != nil || failed.Status != "failed" {
		t.Fatal(failed, e)
	}
	blocker.Definition.Name = "调整后的既有计数名称"
	if _, e = f.Business.Definitions.SaveDraft(ctx, f.Principal, application.SaveDraftInput{Draft: blocker, ExpectedVersion: blocker.Version}); e != nil {
		t.Fatal(e)
	}
	retry := application.RetryTemplateBatchInput{RequestID: "recover-retry", ExpectedVersion: failed.Version}
	recovered, e := f.Business.RetryTemplateBatch(ctx, f.Principal, failed.ID, retry)
	if e != nil || recovered.Status != "prepared" || len(recovered.Drafts) != 1 || len(recovered.Attempts) != 2 {
		t.Fatal(recovered, e)
	}
	replayed, e := f.Business.RetryTemplateBatch(ctx, f.Principal, failed.ID, retry)
	if e != nil || replayed.Version != recovered.Version {
		t.Fatal("successful retry replay", replayed, e)
	}
	if e = f.Store.Write(ctx, func(tx *store.Tx) error { return tx.Delete("draft", blocker.ID) }); e != nil {
		t.Fatal(e)
	}
	instance.Name = "历史身份检查"
	historical, e := f.Business.PrepareTemplateBatch(ctx, f.Principal, model.TemplateBatchInput{ID: "historical-conflict", RequestID: "historical-conflict", GroupID: "factory", Instances: []model.TemplateInstance{instance}})
	if e != nil || historical.Status != "failed" || len(historical.Drafts) != 0 || len(historical.Failures) != 1 || historical.Failures[0].Code != "conflict" {
		t.Fatal("historical identity returned storage failure", historical, e)
	}
	t.Log("unauthorized/protocol/parameter/reviewed-device/reviewed-connector failures produce no drafts; renamed conflict permits atomic recovery with stable retry identity")
}

func TestBusinessHandoverReviewedAlarmDuringContinuousSampling(t *testing.T) {
	f := businessFixture(t, false)
	ctx := context.Background()
	w, e := f.Business.CreateWorkOrder(ctx, f.Principal, application.CreateWorkOrderInput{ID: "live-evidence-work", RequestID: "live-evidence-work", Title: "持续采样交班", GroupID: "factory", AssigneeID: f.Principal.User.ID, AlarmIDs: []string{f.AlarmID}, Reason: "现场交班"})
	if e != nil {
		t.Fatal(e)
	}
	a, e := f.Business.AlarmDetail(ctx, f.Principal, f.AlarmID)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 5; i++ {
		if e = f.Observe(ctx, 35, time.Now().UnixMilli()+int64(i)); e != nil {
			t.Fatal(e)
		}
	}
	w, e = f.Business.HandoverWorkOrder(ctx, f.Principal, w.WorkOrder.ID, application.HandoverInput{RequestID: "live-evidence-handover", ExpectedVersion: w.WorkOrder.Version, FromUserID: f.Principal.User.ID, ToUserID: f.OtherPrincipal.User.ID, PendingItems: []string{"继续检查温度"}, Evidence: []model.HandoverEvidence{{Kind: "alarm", ID: a.Alarm.ID, Version: a.Alarm.Version, ActionVersion: a.ActionVersion, Description: "交班时已阅告警"}}, Reason: "交给下一班次"})
	if e != nil || len(w.Handovers) != 1 || w.WorkOrder.AssigneeID != f.OtherPrincipal.User.ID {
		t.Fatal(w, e)
	}
	t.Log("handover keeps the reviewed alarm version and action version while observation count advances")
}

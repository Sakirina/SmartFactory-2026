package api

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/pkg/model"
)

func TestBusinessHTTPRequiredInputFieldsAndDefaults(t *testing.T) {
	s, f := businessServer(t)
	fields := map[string][]string{
		"AlarmActionInput":                {"request_id", "expected_version", "expected_action_version", "action", "reason"},
		"CreateWorkOrderInput":            {"request_id", "id", "title", "group_id", "assignee_id", "reason"},
		"WorkOrderActionInput":            {"request_id", "expected_version", "action", "reason"},
		"HandoverInput":                   {"request_id", "expected_version", "from_user_id", "to_user_id", "pending_items", "reason"},
		"HandoverEvidence":                {"kind", "id", "version"},
		"SemanticInput":                   {"expected_version"},
		"ImpactInput":                     {"expected_version"},
		"SaveDeviceConfigurationInput":    {"id", "request_id", "expected_version", "parameters"},
		"SaveConnectorConfigurationInput": {"request_id", "expected_version", "group_id", "edge_id", "protocol", "parameters"},
		"RetryTemplateBatchInput":         {"request_id", "expected_version"},
		"Parameters":                      {"kind", "connector_id"},
		"Request":                         {"protocol"},
		"TemplateInstance":                {"id", "name", "template_id", "template_version", "device_id", "device_version", "configuration_id", "configuration_version"},
		"TemplateBatchInput":              {"id", "request_id", "group_id", "instances"},
	}
	type schema struct {
		Required []string `json:"required"`
	}
	var typed struct {
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	raw, err := json.Marshal(DefinitionOpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &typed); err != nil {
		t.Fatal(err)
	}
	for name, required := range fields {
		sort.Strings(required)
		actual := typed.Components.Schemas[name].Required
		sort.Strings(actual)
		if !reflect.DeepEqual(actual, required) {
			t.Fatalf("OpenAPI %s required = %v, want %v", name, actual, required)
		}
		published, ok := publicContracts.Schema(name)
		if !ok {
			t.Fatal("missing JSON Schema", name)
		}
		raw, _ := json.Marshal(published)
		var document struct {
			Definitions map[string]schema `json:"$defs"`
		}
		if err = json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		actual = document.Definitions[name].Required
		sort.Strings(actual)
		if !reflect.DeepEqual(actual, required) {
			t.Fatalf("JSON Schema %s required = %v, want %v", name, actual, required)
		}
	}
	alarm, err := f.Business.AlarmDetail(context.Background(), f.Principal, f.AlarmID)
	if err != nil || alarm.Case.Version != 0 {
		t.Fatal("fixture requires an unhandled alarm", alarm, err)
	}
	ack := application.AlarmActionInput{RequestID: "required-ack", ExpectedVersion: 0, ExpectedActionVersion: alarm.ActionVersion, Action: "acknowledge", Reason: "校验合法的初始处置版本"}
	create := application.CreateWorkOrderInput{RequestID: "required-create", ID: "required-work", Title: "必填字段验证", GroupID: "factory", AssigneeID: f.Principal.User.ID, Reason: "校验省略可选说明与关联对象"}
	change := application.WorkOrderActionInput{RequestID: "required-note", ExpectedVersion: 1, Action: "note", Reason: "工作项备注"}
	handover := application.HandoverInput{RequestID: "required-handover", ExpectedVersion: 1, FromUserID: f.Principal.User.ID, ToUserID: f.OtherPrincipal.User.ID, PendingItems: []string{"下一班次继续检查"}, Evidence: []model.HandoverEvidence{{Kind: "work_order", ID: create.ID, Version: 1}}, Reason: "交接校验"}
	connector := application.SaveConnectorConfigurationInput{RequestID: "required-connector", ExpectedVersion: 0, GroupID: "factory", EdgeID: "edge-a", Protocol: "modbus_tcp", Parameters: deviceconfig.Parameters{Kind: "connector", ConnectorID: "required-connector", Connection: map[string]any{"host": "127.0.0.1", "port": 502}}}
	batch := model.TemplateBatchInput{ID: "required-defaults", RequestID: "required-defaults", GroupID: "factory", Instances: f.Instances}
	device, err := f.Business.DeviceConfiguration(context.Background(), f.Principal, f.Instances[0].DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	requests := []struct {
		schema, path, nested string
		body                 any
	}{
		{"AlarmActionInput", "/alarms/" + f.AlarmID + "/actions", "", ack},
		{"CreateWorkOrderInput", "/work-orders", "", create},
		{"WorkOrderActionInput", "/work-orders/required-work/actions", "", change},
		{"HandoverInput", "/work-orders/required-work/handovers", "", handover},
		{"HandoverEvidence", "/work-orders/required-work/handovers", "evidence", handover},
		{"SemanticInput", "/drafts/" + f.DraftID + "/semantic-diff", "", application.SemanticInput{ExpectedVersion: 2}},
		{"ImpactInput", "/drafts/" + f.DraftID + "/impact", "", application.ImpactInput{ExpectedVersion: 2}},
		{"SaveDeviceConfigurationInput", "/device-configurations", "", application.SaveDeviceConfigurationInput{ID: device.Entity.ID, RequestID: "required-device", ExpectedVersion: device.Entity.Version, Parameters: device.Configuration.Parameters}},
		{"SaveConnectorConfigurationInput", "/connector-configurations", "", connector},
		{"Parameters", "/connector-configurations", "parameters", connector},
		{"Request", "/device-configurations/validate", "", deviceconfig.Request{Protocol: "modbus_tcp", Parameters: &connector.Parameters}},
		{"RetryTemplateBatchInput", "/template-batches/required-defaults/retry", "", application.RetryTemplateBatchInput{RequestID: "required-retry", ExpectedVersion: 1}},
		{"TemplateBatchInput", "/template-batches", "", batch},
		{"TemplateInstance", "/template-batches", "instances", batch},
	}
	missing := 0
	for _, request := range requests {
		for _, field := range fields[request.schema] {
			raw, _ := json.Marshal(request.body)
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			object := body
			if request.nested == "parameters" {
				object = body[request.nested].(map[string]any)
			} else if request.nested != "" {
				object = body[request.nested].([]any)[0].(map[string]any)
			}
			delete(object, field)
			w := call(s, f.Token, "POST", "/api/sf/v1"+request.path, body)
			if w.Code != 422 {
				t.Fatalf("%s missing %s: HTTP %d %s", request.schema, field, w.Code, w.Body.String())
			}
			missing++
		}
	}
	for _, request := range []struct {
		path string
		body any
	}{
		{"/alarms/" + f.AlarmID + "/actions", ack},
		{"/work-orders", create},
		{"/drafts/" + f.DraftID + "/semantic-diff", map[string]any{"expected_version": 2, "published_version": 0}},
		{"/drafts/" + f.DraftID + "/impact", map[string]any{"expected_version": 2}},
		{"/template-batches", batch},
	} {
		w := call(s, f.Token, "POST", "/api/sf/v1"+request.path, request.body)
		if w.Code != 200 {
			t.Fatalf("legal zero/default input %s: HTTP %d %s", request.path, w.Code, w.Body.String())
		}
		if request.path == "/template-batches" {
			var result model.TemplateBatch
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Status != "prepared" || len(result.Drafts) != 25 {
				t.Fatal("omitted template parameters use defaults", result.Status, len(result.Drafts), err)
			}
		}
	}
	handover.Evidence = nil
	w := call(s, f.Token, "POST", "/api/sf/v1/work-orders/required-work/handovers", handover)
	if w.Code != 200 {
		t.Fatal("optional handover evidence omitted", w.Code, w.Body.String())
	}
	s.Mode = "edge"
	w = call(s, f.Token, "POST", "/api/sf/v1/connector-configurations", connector)
	if w.Code != 200 {
		t.Fatal("new connector with explicit expected_version zero", w.Code, w.Body.String())
	}
	t.Logf("%d missing required fields rejected by HTTP; all 14 request and nested schemas agree; explicit initial zero versions, published version zero, default impact budget, omitted template parameters and optional work/handover fields verified", missing)
}

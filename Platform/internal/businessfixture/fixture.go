// Package businessfixture creates isolated, reviewable business workflow data.
package businessfixture

import (
	"context"
	"fmt"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/scenetemplates"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Fixture struct {
	Store          *store.Store
	Identity       *identity.Manager
	Engine         *engine.Service
	Business       *application.Business
	Principal      identity.Principal
	Token          string
	OtherPrincipal identity.Principal
	OtherToken     string
	AlarmID        string
	DraftID        string
	Instances      []model.TemplateInstance
}

func Seed(ctx context.Context, db *store.Store, master []byte, password string) (*Fixture, error) {
	f := &Fixture{Store: db, Identity: &identity.Manager{Store: db, Master: master}, Engine: &engine.Service{Store: db}, Instances: []model.TemplateInstance{}}
	f.Business = &application.Business{Store: db, Identity: f.Identity, Definitions: &application.Definitions{Store: db, Identity: f.Identity, Engine: f.Engine, Mode: "cloud"}, NodeID: db.NodeID, Mode: "edge"}
	put := func(kind, id string, value any) error { _, e := db.Put(ctx, kind, id, 0, value); return e }
	for _, e := range []model.Entity{{ID: "factory", Name: "业务验证工厂", Kind: "asset", Status: "approved", Version: 1}, {ID: "private-factory", Name: "受限资产", Kind: "asset", Status: "approved", Version: 1}, {ID: db.NodeID, Name: "隔离节点", Kind: "edge", ParentID: "factory", Status: "active", Version: 1}, {ID: "private-device", Name: "受限设备", Kind: "device", ParentID: "private-factory", EdgeID: db.NodeID, Status: "approved", Version: 1}} {
		if err := put("entity", e.ID, e); err != nil {
			return nil, err
		}
	}
	if err := put("department", "operations", map[string]any{"id": "operations", "name": "运行部门", "version": 1}); err != nil {
		return nil, err
	}
	for _, id := range []string{"operator-a", "operator-b"} {
		u := model.User{ID: id, Login: id, Name: id, Active: true, Roles: []string{"engineer", "safety"}, Resources: []string{"factory"}, DepartmentID: "operations"}
		if _, err := f.Identity.CreateUser(ctx, model.Actor{}, u, password, "", 0); err != nil {
			return nil, err
		}
	}
	var err error
	f.Token, f.Principal, err = f.Identity.Login(ctx, "operator-a", password, "", false, "business-fixture")
	if err != nil {
		return nil, err
	}
	f.OtherToken, f.OtherPrincipal, err = f.Identity.Login(ctx, "operator-b", password, "", false, "business-fixture")
	if err != nil {
		return nil, err
	}
	for index, template := range scenetemplates.Catalog() {
		deviceID := "device-" + template.ID
		connectorID := "connector-" + template.ID
		connection := map[string]any{}
		switch template.Protocol {
		case "modbus_tcp":
			connection["host"] = "127.0.0.1"
			connection["port"] = 1502
		case "mqtt_device":
			connection["url"] = "tcp://127.0.0.1:1883"
		case "opcua":
			connection["url"] = "opc.tcp://127.0.0.1:4840"
			connection["security_mode"] = "None"
			connection["security_policy"] = "None"
		}
		actions := map[string]any{}
		for i, action := range template.RequiredActions {
			value := map[string]any{"param": "value"}
			switch template.Protocol {
			case "modbus_tcp":
				value["type"] = "write_single_coil"
				value["address"] = i
			case "mqtt_device":
				value["topic"] = "fixture/command"
				value["template"] = "{{.value}}"
			case "opcua":
				value["type"] = "write"
				value["node_id"] = fmt.Sprintf("ns=2;s=%s", action)
				value["data_type"] = "bool"
			}
			actions[action] = value
		}
		_, err = f.Business.SaveConnectorConfiguration(ctx, f.Principal, application.SaveConnectorConfigurationInput{RequestID: "seed-connector-" + template.ID, GroupID: "factory", EdgeID: db.NodeID, Protocol: template.Protocol, Parameters: deviceconfig.Parameters{Kind: "connector", ConnectorID: connectorID, Connection: connection, Polling: map[string]any{"interval_millis": 1000}, Converter: map[string]any{"action_mappings": actions}}})
		if err != nil {
			return nil, fmt.Errorf("seed connector %s: %w", template.ID, err)
		}
		points := []map[string]any{}
		for i, key := range template.RequiredKeys {
			p := map[string]any{"key": key}
			switch template.Protocol {
			case "modbus_tcp":
				p["register_type"] = "holding_register"
				p["address"] = i
				p["data_type"] = "uint16"
			case "mqtt_device":
				p["source"] = key
			case "opcua":
				p["node_id"] = fmt.Sprintf("ns=2;s=%s", key)
			}
			points = append(points, p)
		}
		configuration, e := deviceconfig.Validate(deviceconfig.Request{Protocol: template.Protocol, DeviceID: deviceID, Parameters: &deviceconfig.Parameters{Kind: "device", ConnectorID: connectorID, DeviceID: deviceID, DeviceName: template.Name, Datapoints: points}})
		if e != nil {
			return nil, e
		}
		if !configuration.Valid {
			return nil, fmt.Errorf("seed device invalid: %+v", configuration.Issues)
		}
		entity := model.Entity{ID: deviceID, Name: template.Name, Kind: "device", ParentID: "factory", EdgeID: db.NodeID, Status: "approved", Protocol: template.Protocol, Version: 1, Config: configuration.Config}
		if err = put("entity", deviceID, entity); err != nil {
			return nil, err
		}
		f.Instances = append(f.Instances, model.TemplateInstance{ID: fmt.Sprintf("scene-%d", index+1), Name: template.Name, TemplateID: template.ID, TemplateVersion: template.Version, DeviceID: deviceID, DeviceVersion: 1, ConfigurationID: db.NodeID + "/" + connectorID, ConfigurationVersion: 1, SafetyUserID: f.Principal.User.ID, Parameters: map[string]any{}})
	}
	f.Business.Mode = "cloud"
	d := model.Definition{ID: "fixture-alarm", Name: "温度告警", SchemaVersion: model.ContractVersion, Kind: "alarm", GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"device-climate-ventilation"}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "threshold", Type: "hysteresis", Params: map[string]any{"high": 30, "low": 27}}, {ID: "alarm", Type: "alarm", Params: map[string]any{"severity": "MAJOR"}}}, Connections: []model.Connection{{From: "input", To: "threshold"}, {From: "threshold", To: "alarm"}}, Policy: model.Policy{Channels: []string{"in_app"}, Recipients: []string{"site"}}}
	draft, err := f.Business.Definitions.SaveDraft(ctx, f.Principal, application.SaveDraftInput{Draft: model.Draft{ID: d.ID + "-draft", Definition: d}})
	if err != nil {
		return nil, err
	}
	f.DraftID = draft.ID
	version := draft.Version
	if _, err = f.Business.Definitions.PublishDraft(ctx, f.Principal, application.DraftInput{ID: draft.ID, ExpectedVersion: &version}); err != nil {
		return nil, err
	}
	if err = f.Observe(ctx, 35, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	alarms, err := db.List(ctx, "alarm")
	if err != nil {
		return nil, err
	}
	if len(alarms) != 1 {
		return nil, fmt.Errorf("fixture alarm count %d", len(alarms))
	}
	f.AlarmID = alarms[0].ID
	return f, nil
}

func (f *Fixture) Observe(ctx context.Context, value any, at int64) error {
	return f.Engine.Process(ctx, model.Observation{ID: fmt.Sprintf("fixture-%d", at), MessageID: fmt.Sprintf("message-%d", at), SourceID: f.Store.NodeID, DeviceID: "device-climate-ventilation", Key: "temperature", Value: value, ObservedMS: at, ReceivedMS: at, Quality: "GOOD", EntityRevision: 1}, false, "")
}

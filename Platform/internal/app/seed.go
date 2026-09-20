package app

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"competition2026/product/platform/internal/plugins"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (a *Application) Seed(ctx context.Context, password string) error {
	if _, e := a.Store.Get(ctx, "installation", "example_factory"); e == nil {
		if e = a.seedDefinitions(ctx); e != nil {
			return e
		}
		return a.seedDashboard(ctx)
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	actor := model.Actor{UserID: "bootstrap", Source: "simulation-fixture"}
	if e := plugins.SyncOrganization(ctx, a.Store, actor, plugins.OrganizationSync{ID: "demo-organization", Source: "example-hr", Sequence: 1, Full: false, Departments: []plugins.Department{{ID: "engineering", Name: "工程技术部", ParentID: "production", ManagerID: "leader"}, {ID: "production", Name: "生产管理部", ManagerID: "leader"}, {ID: "safety", Name: "现场安全组", ParentID: "production", ManagerID: "safety"}}}); e != nil {
		return e
	}
	users := []model.User{{ID: "engineer", Login: "engineer", Name: "工程师一", Roles: []string{"engineer"}, DepartmentID: "engineering"}, {ID: "engineer2", Login: "engineer2", Name: "工程师二", Roles: []string{"engineer"}, DepartmentID: "engineering"}, {ID: "leader", Login: "leader", Name: "生产负责人", Roles: []string{"leader"}, DepartmentID: "production"}, {ID: "safety", Login: "safety", Name: "现场安全员", Roles: []string{"safety"}, DepartmentID: "safety"}, {ID: "viewer", Login: "viewer", Name: "运行观察员", Roles: []string{"viewer"}, DepartmentID: "production"}, {ID: "assistant", Login: "assistant", Name: "AI 草稿助手", Roles: []string{"ai"}, AI: true, DepartmentID: "engineering"}}
	for _, u := range users {
		u.Active = true
		u.Resources = []string{"factory"}
		u.Teams = []string{}
		totp := ""
		if u.ID == "safety" {
			totp = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
		}
		if _, e := a.Server.Identity.CreateUser(ctx, actor, u, password, totp, 0); e != nil {
			return e
		}
	}
	entities := []model.Entity{{ID: "factory", Name: "示例工厂", Kind: "asset", Status: "active", Version: 1}, {ID: "workshop", Name: "生产车间", Kind: "asset", ParentID: "factory", Status: "active", Version: 1}, {ID: "warehouse", Name: "仓储区域", Kind: "asset", ParentID: "factory", Status: "active", Version: 1}, {ID: "edge-a", Name: "车间边缘节点", Kind: "edge", ParentID: "factory", Status: "active", Version: 1}, {ID: "edge-b", Name: "仓储边缘节点", Kind: "edge", ParentID: "factory", Status: "active", Version: 1}, {ID: "edge-c", Name: "安全边缘节点", Kind: "edge", ParentID: "factory", Status: "active", Version: 1}}
	for i, entry := range []struct{ id, name, parent, protocol string }{{"climate-1", "温湿度与通风设备", "workshop", "modbus"}, {"light-1", "红外照明设备", "workshop", "mqtt"}, {"gas-1", "危气监测设备", "workshop", "opcua"}, {"agv-1", "AGV 避障设备", "warehouse", "modbus"}, {"counter-1", "货物计数设备", "warehouse", "mqtt"}} {
		entities = append(entities, model.Entity{ID: entry.id, Name: entry.name, Kind: "device", ParentID: entry.parent, EdgeID: "edge-a", Protocol: entry.protocol, Status: "approved", SamplingMS: 1000, Version: 1, Tags: map[string]string{"scene": fmt.Sprint(i + 1), "fixture": "simulation"}})
	}
	for _, entity := range entities {
		if _, e := a.Store.Put(ctx, "entity", entity.ID, 0, entity); e != nil {
			return e
		}
	}
	if e := a.seedDefinitions(ctx); e != nil {
		return e
	}
	if e := a.seedDashboard(ctx); e != nil {
		return e
	}
	if a.Options.Mode == "edge" {
		bundle, e := a.Server.Identity.SignBundle(ctx, a.Options.NodeID, 1)
		if e != nil {
			return e
		}
		if e = a.Server.Identity.ApplyBundle(ctx, bundle, a.Store.SignKey.Public().(ed25519.PublicKey)); e != nil {
			return e
		}
	}
	_, e := a.Store.Put(ctx, "installation", "example_factory", 0, map[string]any{"installed_ms": time.Now().UnixMilli(), "source": "editable example factory", "devices": len(entities) - 6})
	return e
}

func (a *Application) seedDashboard(ctx context.Context) error {
	if a.Options.Mode == "edge" && a.Options.SyncURL != "" {
		return nil
	}
	if _, e := a.Store.Get(ctx, "dashboard", "factory"); e == nil {
		return nil
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	d := model.Dashboard{ID: "factory", Title: "示例工厂 · 生产安全运行", GroupID: "factory", Version: 1, DeviceIDs: []string{"climate-1", "light-1", "gas-1", "agv-1", "counter-1"}, Keys: []string{}, WindowMS: 3600000, RefreshMS: 1000, ShowSources: true, ShowAlarms: true, Metrics: []model.DashboardMetric{{DeviceID: "climate-1", Key: "temperature", Label: "车间温度"}, {DeviceID: "climate-1", Key: "humidity", Label: "车间湿度"}, {DeviceID: "gas-1", Key: "co", Label: "一氧化碳"}, {DeviceID: "light-1", Key: "light", Label: "现场照明"}, {DeviceID: "agv-1", Key: "distance", Label: "AGV 障碍距离"}, {DeviceID: "counter-1", Key: "total", Label: "货物累计计数"}}}
	_, e := a.Store.Put(ctx, "dashboard", d.ID, 0, d)
	return e
}
func (a *Application) seedDefinitions(ctx context.Context) error {
	if a.Options.Mode == "edge" && a.Options.SyncURL != "" {
		return nil
	}
	actor := model.Actor{UserID: "bootstrap", Source: "simulation-fixture"}
	defs := ExampleDefinitions()
	for _, d := range defs {
		if _, e := a.Store.Get(ctx, "definition", d.ID); e == nil {
			continue
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		d.SchemaVersion = "1.0"
		d.GroupID = "factory"
		d.Status = "draft"
		draft := model.Draft{ID: d.ID, Definition: d}
		if _, e := a.Server.Engine.SaveDraft(ctx, actor, draft, 0); e != nil {
			return e
		}
		if _, e := a.Server.Engine.Publish(ctx, actor, d.ID); e != nil {
			return e
		}
	}
	return nil
}
func ExampleDefinitions() []model.Definition {
	analysis := model.Definition{ID: "climate-average", Name: "车间温度一分钟平均值", Kind: "analysis", Selector: model.Selector{DeviceIDs: []string{"climate-1"}, Keys: []string{"temperature"}, WindowMS: 60000}, Nodes: []model.Node{{ID: "source", Type: "input", Label: "温度读数"}, {ID: "average", Type: "aggregate", Label: "一分钟平均值", Params: map[string]any{"function": "avg"}}, {ID: "output", Type: "output", Label: "保存计算结果"}}, Connections: []model.Connection{{From: "source", To: "average"}, {From: "average", To: "output"}}, Outputs: []model.Output{{Key: "temperature", Type: "number", Unit: "°C", NodeID: "output", Description: "只使用 GOOD 数据计算的窗口平均温度"}}}
	alarm := model.Definition{ID: "climate-alarm", Name: "温度超限与回差恢复", Kind: "alarm", Selector: model.Selector{DeviceIDs: []string{"climate-1"}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "source", Type: "input", Label: "温度读数"}, {ID: "threshold", Type: "hysteresis", Label: "超过 30°C，低于 27°C 恢复", Params: map[string]any{"high": 30, "low": 27}}, {ID: "alarm", Type: "alarm", Label: "通知现场人员", Params: map[string]any{"severity": "WARNING"}}}, Connections: []model.Connection{{From: "source", To: "threshold"}, {From: "threshold", To: "alarm"}}, Policy: model.Policy{Channels: []string{"in_app"}, Recipients: []string{"site"}, LateNotification: "in_app"}}
	strategy := model.Definition{ID: "ventilation-plan", Name: "车间通风调节预案", Kind: "strategy", Selector: model.Selector{DeviceIDs: []string{"climate-1"}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "source", Type: "input", Label: "现场温度"}, {ID: "threshold", Type: "threshold", Label: "温度超过 30°C", Params: map[string]any{"operator": ">", "value": 30}}, {ID: "action", Type: "action", Label: "启用通风"}}, Connections: []model.Connection{{From: "source", To: "threshold"}, {From: "threshold", To: "action"}}, Policy: model.Policy{EdgeIDs: []string{"edge-a"}, RiskCategory: "business", RiskLevel: 2, SafetyUserID: "safety", Conditions: []model.Condition{{DeviceID: "climate-1", Key: "interlock", Operator: "==", Value: false, Interlock: true, MaxAgeMS: 5000}}, Steps: []model.Step{{ID: "fan-on", EdgeID: "edge-a", DeviceID: "climate-1", Action: "set_fan", Params: map[string]string{"value": "true"}, Idempotent: true, TimeoutMS: 5000}}}}
	counter := model.Definition{ID: "goods-count", Name: "货物有效计数", Kind: "analysis", Selector: model.Selector{DeviceIDs: []string{"counter-1"}, Keys: []string{"pulse"}}, Nodes: []model.Node{{ID: "input", Type: "input", Label: "光电事件"}, {ID: "count", Type: "counter", Label: "累计有效事件", Params: map[string]any{"mode": "delta"}}, {ID: "output", Type: "output", Label: "保存累计值"}}, Connections: []model.Connection{{From: "input", To: "count"}, {From: "count", To: "output"}}, Outputs: []model.Output{{Key: "total", Type: "integer", Unit: "件", NodeID: "output"}}}
	return append([]model.Definition{analysis, alarm, strategy, counter}, SceneDefinitions()...)
}

package app

import (
	"competition2026/product/platform/pkg/model"
	"fmt"
)

func sceneAlarm(id, name, device, field string, high, low float64, below bool) model.Definition {
	params := map[string]any{"high": high, "low": low}
	if below {
		params["direction"] = "below"
	}
	return model.Definition{ID: id, Name: name, Kind: "alarm", Selector: model.Selector{DeviceIDs: []string{device}, Keys: []string{field}}, Nodes: []model.Node{{ID: "input", Type: "input", Label: "现场读数"}, {ID: "range", Type: "hysteresis", Label: "阈值与恢复回差", Params: params}, {ID: "alarm", Type: "alarm", Label: "异常通知", Params: map[string]any{"severity": "WARNING"}}}, Connections: []model.Connection{{From: "input", To: "range"}, {From: "range", To: "alarm"}}, Policy: model.Policy{Channels: []string{"in_app"}, Recipients: []string{"site"}, LateNotification: "in_app"}}
}
func sceneControl(id, name, device, field, expression, action string, value bool, delay int64) model.Definition {
	nodes := []model.Node{{ID: "input", Type: "input", Label: "现场读数"}, {ID: "condition", Type: "expression", Label: "现场判断", Params: map[string]any{"code": expression}}}
	connections := []model.Connection{{From: "input", To: "condition"}}
	last := "condition"
	if delay > 0 {
		nodes = append(nodes, model.Node{ID: "delay", Type: "debounce", Label: "持续时间确认", Params: map[string]any{"duration_ms": delay, "mode": "activation"}})
		connections = append(connections, model.Connection{From: last, To: "delay"})
		last = "delay"
	}
	nodes = append(nodes, model.Node{ID: "action", Type: "action", Label: "执行设备动作"})
	connections = append(connections, model.Connection{From: last, To: "action"})
	return model.Definition{ID: id, Name: name, Kind: "strategy", Selector: model.Selector{DeviceIDs: []string{device}, Keys: []string{field}}, Nodes: nodes, Connections: connections, Policy: model.Policy{EdgeIDs: []string{"edge-a"}, RiskCategory: "business", RiskLevel: 1, SafetyUserID: "safety", Conditions: []model.Condition{{DeviceID: device, Key: "interlock", Operator: "==", Value: false, Interlock: true, MaxAgeMS: 5000}}, Steps: []model.Step{{ID: action, EdgeID: "edge-a", DeviceID: device, Action: action, Params: map[string]string{"value": fmt.Sprint(value)}, Idempotent: true, TimeoutMS: 5000}}}}
}

// SceneDefinitions are editable typed definitions used by all three surfaces.
func SceneDefinitions() []model.Definition {
	defs := []model.Definition{
		sceneAlarm("climate-low", "温度下限与回差恢复", "climate-1", "temperature", 20, 18, true),
		sceneAlarm("humidity-high", "湿度上限与回差恢复", "climate-1", "humidity", 70, 65, false),
		sceneAlarm("humidity-low", "湿度下限与回差恢复", "climate-1", "humidity", 35, 30, true),
		sceneControl("ventilation-off", "温度恢复后关闭通风", "climate-1", "temperature", "good && fresh && value <= 27", "set_fan", false, 0),
		sceneControl("heating-on", "低温加热", "climate-1", "temperature", "good && fresh && value <= 18", "set_heater", true, 0),
		sceneControl("heating-off", "温度恢复后关闭加热", "climate-1", "temperature", "good && fresh && value >= 20", "set_heater", false, 0),
		sceneControl("humidifier-on", "低湿加湿", "climate-1", "humidity", "good && fresh && value <= 30", "set_humidifier", true, 0),
		sceneControl("humidifier-off", "湿度恢复后关闭加湿", "climate-1", "humidity", "good && fresh && value >= 35", "set_humidifier", false, 0),
		sceneControl("dehumidifier-on", "高湿除湿", "climate-1", "humidity", "good && fresh && value >= 70", "set_dehumidifier", true, 0),
		sceneControl("dehumidifier-off", "湿度恢复后关闭除湿", "climate-1", "humidity", "good && fresh && value <= 65", "set_dehumidifier", false, 0),
		sceneControl("light-on", "有人时点亮照明", "light-1", "presence", "good && fresh && value", "set_light", true, 100),
		sceneControl("light-off", "无人持续两秒关闭照明", "light-1", "presence", "good && fresh && !value", "set_light", false, 2000),
	}
	stop := sceneControl("agv-stop", "障碍、过期或无效距离触发停车", "agv-1", "distance", "!good || !fresh || value <= 50", "stop", true, 0)
	stop.Policy.Watchdog = true
	stop.Policy.RiskCategory = "safety"
	stop.Policy.RiskLevel = 3
	resume := sceneControl("agv-resume", "距离恢复后允许继续行驶", "agv-1", "distance", "false", "resume", false, 0)
	resume.Policy.Conditions = append(resume.Policy.Conditions, model.Condition{DeviceID: "agv-1", Key: "distance", Operator: ">=", Value: 80, MaxAgeMS: 5000})
	defs = append(defs, stop, resume)
	for _, gas := range []struct {
		key, name string
		high, low float64
	}{{"smoke", "烟雾", 5, 2}, {"combustible", "可燃气", 20, 10}, {"co", "一氧化碳", 30, 15}} {
		alarm := sceneAlarm("gas-"+gas.key+"-alarm", gas.name+"超限与恢复", "gas-1", gas.key, gas.high, gas.low, false)
		alarm.Nodes[2].Params["severity"] = "CRITICAL"
		control := sceneControl("gas-"+gas.key+"-response", gas.name+"排风处置", "gas-1", gas.key, fmt.Sprintf("good && fresh && value >= %g", gas.high), "set_extractor", true, 0)
		control.Policy.RiskCategory = "safety"
		control.Policy.RiskLevel = 3
		defs = append(defs, alarm, control)
	}
	return defs
}

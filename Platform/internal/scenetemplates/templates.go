// Package scenetemplates instantiates the five editable factory scenarios.
package scenetemplates

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"competition2026/product/platform/pkg/model"
	"competition2026/product/platform/pkg/precise"
)

type Template struct {
	ID                string                    `json:"id"`
	Version           int64                     `json:"version"`
	Name              string                    `json:"name"`
	Protocol          string                    `json:"protocol"`
	RequiredKeys      []string                  `json:"required_keys"`
	RequiredActions   []string                  `json:"required_actions"`
	Parameters        []model.ParameterMetadata `json:"parameters"`
	DefinitionCount   int                       `json:"definition_count"`
	AvailableVersions []int64                   `json:"available_versions"`
}

func param(key, label, unit string, value any, min, max int64) model.ParameterMetadata {
	return model.ParameterMetadata{Key: key, Label: label, Type: "number", Default: value, Minimum: &min, Maximum: &max, Unit: unit, Description: label}
}
func Catalog() []Template {
	all := []Template{
		{ID: "climate-ventilation", Version: 1, Name: "温湿度与通风", Protocol: "modbus_tcp", RequiredKeys: []string{"temperature", "humidity", "interlock"}, RequiredActions: []string{"set_fan", "set_heater", "set_humidifier", "set_dehumidifier"}, DefinitionCount: 14, Parameters: []model.ParameterMetadata{param("temperature_high", "温度上限", "°C", 30, -50, 200), param("temperature_recovery", "高温恢复值", "°C", 27, -50, 200), param("temperature_low", "温度下限", "°C", 18, -50, 200), param("temperature_low_recovery", "低温恢复值", "°C", 20, -50, 200), param("humidity_high", "湿度上限", "%", 70, 0, 100), param("humidity_recovery", "高湿恢复值", "%", 65, 0, 100), param("humidity_low", "湿度下限", "%", 30, 0, 100), param("humidity_low_recovery", "低湿恢复值", "%", 35, 0, 100)}},
		{ID: "infrared-lighting", Version: 1, Name: "红外照明", Protocol: "mqtt_device", RequiredKeys: []string{"presence", "interlock"}, RequiredActions: []string{"set_light"}, DefinitionCount: 2, Parameters: []model.ParameterMetadata{integerParam("on_delay_ms", "有人开灯确认时间", "ms", 100, 1, 86400000), integerParam("off_delay_ms", "无人关灯等待时间", "ms", 2000, 1, 86400000)}},
		{ID: "hazardous-gas", Version: 1, Name: "危气监测", Protocol: "opcua", RequiredKeys: []string{"smoke", "combustible", "co", "interlock"}, RequiredActions: []string{"set_extractor"}, DefinitionCount: 6, Parameters: []model.ParameterMetadata{param("smoke_high", "烟雾上限", "", 5, 0, 10000), param("smoke_recovery", "烟雾恢复值", "", 2, 0, 10000), param("combustible_high", "可燃气上限", "%LEL", 20, 0, 100), param("combustible_recovery", "可燃气恢复值", "%LEL", 10, 0, 100), param("co_high", "一氧化碳上限", "ppm", 30, 0, 10000), param("co_recovery", "一氧化碳恢复值", "ppm", 15, 0, 10000)}},
		{ID: "agv-obstacle", Version: 1, Name: "AGV 避障", Protocol: "modbus_tcp", RequiredKeys: []string{"distance", "interlock"}, RequiredActions: []string{"stop", "resume"}, DefinitionCount: 2, Parameters: []model.ParameterMetadata{param("stop_distance", "停车距离", "cm", 50, 0, 100000), param("resume_distance", "恢复距离", "cm", 80, 0, 100000)}},
		{ID: "goods-counting", Version: 1, Name: "货物计数", Protocol: "mqtt_device", RequiredKeys: []string{"pulse"}, RequiredActions: []string{}, DefinitionCount: 1, Parameters: []model.ParameterMetadata{{Key: "mode", Label: "计数方式", Type: "string", Default: "delta", Enum: []string{"count", "delta", "rising"}, Description: "按有效观测、输入增量或上升沿累计，结果保留精确整数"}}},
	}
	for i := range all {
		all[i].AvailableVersions = []int64{1, 2}
	}
	return all
}
func integerParam(key, label, unit string, value, min, max int64) model.ParameterMetadata {
	p := param(key, label, unit, value, min, max)
	p.Type = "integer"
	return p
}

func Get(id string, version int64) (Template, error) {
	for _, t := range Catalog() {
		if t.ID == id && (version == 1 || version == 2) {
			if version == 2 {
				t.Version = 2
				if len(t.RequiredActions) > 0 {
					t.Parameters = append(t.Parameters, integerParam("freshness_ms", "控制输入和互锁允许的数据年龄", "ms", 5000, 100, 60000))
				}
				if t.ID == "climate-ventilation" {
					t.Parameters = append(t.Parameters, integerParam("average_window_ms", "温湿度平均窗口", "ms", 60000, 1000, 86400000))
				}
				if t.ID == "goods-counting" {
					t.Parameters = append(t.Parameters, integerParam("count_window_ms", "计数窗口，0 表示累计全部有效观测", "ms", 0, 0, 86400000))
				}
			}
			return t, nil
		}
	}
	return Template{}, errors.New("template identity or version is unavailable")
}

func Parameters(t Template, input map[string]any) (map[string]any, error) {
	result := map[string]any{}
	allowed := map[string]bool{}
	for _, p := range t.Parameters {
		allowed[p.Key] = true
		value, ok := input[p.Key]
		if !ok {
			value = p.Default
		}
		if value == nil {
			return nil, fmt.Errorf("parameter %s is required", p.Key)
		}
		if p.Type == "string" {
			str, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("parameter %s requires a string", p.Key)
			}
			found := len(p.Enum) == 0
			for _, v := range p.Enum {
				found = found || v == str
			}
			if !found {
				return nil, fmt.Errorf("parameter %s is outside its enumeration", p.Key)
			}
		} else {
			n, ok := precise.Number(value)
			if !ok || p.Type == "integer" && !n.IsInt() {
				return nil, fmt.Errorf("parameter %s requires %s", p.Key, p.Type)
			}
			if p.Minimum != nil {
				min, _ := precise.Number(*p.Minimum)
				if n.Cmp(min) < 0 {
					return nil, fmt.Errorf("parameter %s is below its minimum", p.Key)
				}
			}
			if p.Maximum != nil {
				max, _ := precise.Number(*p.Maximum)
				if n.Cmp(max) > 0 {
					return nil, fmt.Errorf("parameter %s exceeds its maximum", p.Key)
				}
			}
		}
		result[p.Key] = value
	}
	for key := range input {
		if !allowed[key] {
			return nil, fmt.Errorf("unknown template parameter %s", key)
		}
	}
	for _, pair := range [][2]string{{"temperature_recovery", "temperature_high"}, {"temperature_low", "temperature_low_recovery"}, {"humidity_recovery", "humidity_high"}, {"humidity_low", "humidity_low_recovery"}, {"smoke_recovery", "smoke_high"}, {"combustible_recovery", "combustible_high"}, {"co_recovery", "co_high"}, {"stop_distance", "resume_distance"}} {
		low, lok := result[pair[0]]
		high, hok := result[pair[1]]
		if lok && hok {
			a, _ := precise.Number(low)
			b, _ := precise.Number(high)
			if a.Cmp(b) >= 0 {
				return nil, fmt.Errorf("parameter %s must be lower than %s", pair[0], pair[1])
			}
		}
	}
	return result, nil
}

func Instantiate(t Template, prefix, name, group, device, edge, safety string, input map[string]any) ([]model.Definition, error) {
	p, err := Parameters(t, input)
	if err != nil {
		return nil, err
	}
	if prefix == "" || name == "" || group == "" || device == "" || edge == "" {
		return nil, errors.New("instance identity, name, group, device and edge are required")
	}
	all := []model.Definition{}
	add := func(d model.Definition) {
		d.ID = prefix + "/" + d.ID
		d.Name = name + " · " + d.Name
		d.GroupID = group
		d.SchemaVersion = model.ContractVersion
		d.Status = "draft"
		d.Version = 0
		d.Dependencies = []string{}
		d.Outputs = append([]model.Output{}, d.Outputs...)
		d.Selector.DeviceIDs = []string{device}
		if d.Kind == "strategy" {
			d.Policy.EdgeIDs = []string{edge}
			d.Policy.SafetyUserID = safety
			d.Policy.Conditions = []model.Condition{{DeviceID: device, Key: "interlock", Operator: "==", Value: false, Interlock: true, MaxAgeMS: 5000}}
			for i := range d.Policy.Steps {
				d.Policy.Steps[i].DeviceID = device
				d.Policy.Steps[i].EdgeID = edge
			}
		}
		all = append(all, d)
	}
	scalar := func(key string) string { raw, _ := json.Marshal(p[key]); return string(raw) }
	control := func(id, title, key, expression, action, value string, delay int64, safetyRisk bool) {
		d := controlDefinition(id, title, key, expression, action, value, delay)
		if safetyRisk {
			d.Policy.RiskCategory = "safety"
			d.Policy.RiskLevel = 3
		}
		add(d)
	}
	switch t.ID {
	case "climate-ventilation":
		add(analysisDefinition("temperature-average", "一分钟平均温度", "temperature", "aggregate", map[string]any{"function": "avg"}, "temperature", "number", "°C"))
		for _, x := range []struct {
			id, title, key, high, low string
			below                     bool
		}{{"temperature-high", "高温告警", "temperature", "temperature_high", "temperature_recovery", false}, {"temperature-low", "低温告警", "temperature", "temperature_low_recovery", "temperature_low", true}, {"humidity-high", "高湿告警", "humidity", "humidity_high", "humidity_recovery", false}, {"humidity-low", "低湿告警", "humidity", "humidity_low_recovery", "humidity_low", true}} {
			add(alarmDefinition(x.id, x.title, x.key, p[x.high], p[x.low], x.below, "WARNING"))
		}
		for _, x := range []struct{ id, title, key, op, param, action, value string }{{"fan-on", "高温通风", "temperature", ">=", "temperature_high", "set_fan", "true"}, {"fan-off", "温度恢复关闭通风", "temperature", "<=", "temperature_recovery", "set_fan", "false"}, {"heater-on", "低温加热", "temperature", "<=", "temperature_low", "set_heater", "true"}, {"heater-off", "温度恢复关闭加热", "temperature", ">=", "temperature_low_recovery", "set_heater", "false"}, {"humidifier-on", "低湿加湿", "humidity", "<=", "humidity_low", "set_humidifier", "true"}, {"humidifier-off", "湿度恢复关闭加湿", "humidity", ">=", "humidity_low_recovery", "set_humidifier", "false"}, {"dehumidifier-on", "高湿除湿", "humidity", ">=", "humidity_high", "set_dehumidifier", "true"}, {"dehumidifier-off", "湿度恢复关闭除湿", "humidity", "<=", "humidity_recovery", "set_dehumidifier", "false"}} {
			control(x.id, x.title, x.key, "good && fresh && value "+x.op+" "+scalar(x.param), x.action, x.value, 0, false)
		}
		add(analysisDefinition("humidity-average", "一分钟平均湿度", "humidity", "aggregate", map[string]any{"function": "avg"}, "humidity", "number", "%"))
	case "infrared-lighting":
		on, _ := precise.Number(p["on_delay_ms"])
		off, _ := precise.Number(p["off_delay_ms"])
		control("light-on", "有人开启照明", "presence", "good && fresh && value", "set_light", "true", on.Num().Int64(), false)
		control("light-off", "无人等待后关闭照明", "presence", "good && fresh && !value", "set_light", "false", off.Num().Int64(), false)
	case "hazardous-gas":
		for _, gas := range []struct{ key, name string }{{"smoke", "烟雾"}, {"combustible", "可燃气"}, {"co", "一氧化碳"}} {
			add(alarmDefinition(gas.key+"-alarm", gas.name+"超限与恢复", gas.key, p[gas.key+"_high"], p[gas.key+"_recovery"], false, "CRITICAL"))
			control(gas.key+"-response", gas.name+"排风处理", gas.key, "good && fresh && value >= "+scalar(gas.key+"_high"), "set_extractor", "true", 0, true)
		}
	case "agv-obstacle":
		control("stop", "障碍或数据无效时停车", "distance", "!good || !fresh || value <= "+scalar("stop_distance"), "stop", "true", 0, true)
		all[len(all)-1].Policy.Watchdog = true
		control("resume", "距离恢复后人工继续行驶", "distance", "false", "resume", "false", 0, false)
		all[len(all)-1].Policy.Conditions = append(all[len(all)-1].Policy.Conditions, model.Condition{DeviceID: device, Key: "distance", Operator: ">=", Value: p["resume_distance"], MaxAgeMS: 5000})
	case "goods-counting":
		add(analysisDefinition("total", "货物有效累计计数", "pulse", "counter", map[string]any{"mode": p["mode"]}, "total", "integer", "件"))
		all[0].Selector.WindowMS = 0
	default:
		return nil, errors.New("unsupported template")
	}
	if len(all) != t.DefinitionCount {
		return nil, fmt.Errorf("template definition count mismatch: %d", len(all))
	}
	if t.Version == 2 {
		for i := range all {
			d := &all[i]
			if d.Kind == "strategy" {
				n, _ := precise.Number(p["freshness_ms"])
				d.Policy.FreshnessMS = n.Num().Int64()
				for j := range d.Policy.Conditions {
					d.Policy.Conditions[j].MaxAgeMS = d.Policy.FreshnessMS
				}
			}
			if d.Kind == "analysis" && t.ID == "climate-ventilation" {
				n, _ := precise.Number(p["average_window_ms"])
				d.Selector.WindowMS = n.Num().Int64()
			}
			if t.ID == "goods-counting" {
				n, _ := precise.Number(p["count_window_ms"])
				d.Selector.WindowMS = n.Num().Int64()
			}
		}
	}
	return all, nil
}

func analysisDefinition(id, name, key, node string, params map[string]any, output, typ, unit string) model.Definition {
	return model.Definition{ID: id, Name: name, Kind: "analysis", Selector: model.Selector{Keys: []string{key}, WindowMS: 60000}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "calculation", Type: node, Params: params}, {ID: "output", Type: "output"}}, Connections: []model.Connection{{From: "input", To: "calculation"}, {From: "calculation", To: "output"}}, Outputs: []model.Output{{Key: output, Type: typ, Unit: unit, NodeID: "output"}}}
}
func alarmDefinition(id, name, key string, high, low any, below bool, severity string) model.Definition {
	params := map[string]any{"high": high, "low": low}
	if below {
		params["direction"] = "below"
	}
	return model.Definition{ID: id, Name: name, Kind: "alarm", Selector: model.Selector{Keys: []string{key}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "range", Type: "hysteresis", Params: params}, {ID: "alarm", Type: "alarm", Params: map[string]any{"severity": severity}}}, Connections: []model.Connection{{From: "input", To: "range"}, {From: "range", To: "alarm"}}, Policy: model.Policy{Channels: []string{"in_app"}, Recipients: []string{"site"}, LateNotification: "in_app"}}
}
func controlDefinition(id, name, key, expression, action, value string, delay int64) model.Definition {
	d := model.Definition{ID: id, Name: name, Kind: "strategy", Selector: model.Selector{Keys: []string{key}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "condition", Type: "expression", Params: map[string]any{"code": strings.TrimSpace(expression)}}}, Connections: []model.Connection{{From: "input", To: "condition"}}, Policy: model.Policy{RiskCategory: "business", RiskLevel: 1, Steps: []model.Step{{ID: action, Action: action, Params: map[string]string{"value": value}, Idempotent: true, TimeoutMS: 5000}}}}
	last := "condition"
	if delay > 0 {
		d.Nodes = append(d.Nodes, model.Node{ID: "delay", Type: "debounce", Params: map[string]any{"duration_ms": delay, "mode": "activation"}})
		d.Connections = append(d.Connections, model.Connection{From: last, To: "delay"})
		last = "delay"
	}
	d.Nodes = append(d.Nodes, model.Node{ID: "action", Type: "action"})
	d.Connections = append(d.Connections, model.Connection{From: last, To: "action"})
	return d
}

package deviceconfig

import (
	"encoding/json"
	"fmt"
	"strings"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/platform/pkg/precise"
	"google.golang.org/protobuf/encoding/protojson"
)

func reportParameters(r *dt.ReportStrategyConfig) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{"mode": r.Mode.String(), "period_seconds": int64(r.PeriodSeconds), "deadband": r.Deadband}
}
func reportProto(p map[string]any) (*dt.ReportStrategyConfig, error) {
	if len(p) == 0 {
		return nil, nil
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return nil, e
	}
	r := &dt.ReportStrategyConfig{}
	e = protojson.Unmarshal(raw, r)
	return r, e
}
func reportFields() []Field {
	return []Field{field("mode", "上报策略", "string", "", "与 DataTransfer 上报模式一致", false, nil, nil, "STRATEGY_UNSPECIFIED", "ON_RECEIVED", "ON_CHANGE", "ON_REPORT_PERIOD", "ON_CHANGE_OR_REPORT_PERIOD"), field("period_seconds", "上报周期", "integer", "s", "周期模式要求正整数，其余模式允许零", false, bound(0), bound(86400)), field("deadband", "变化阈值", "number", "", "数值变化达到此阈值时上报", false, bound(0), nil)}
}
func checkReportParameters(p Parameters, add func(string, string, string)) {
	check := func(path string, value map[string]any) {
		checkFields(path, value, reportFields(), add)
		mode, _ := value["mode"].(string)
		if mode == "ON_REPORT_PERIOD" || mode == "ON_CHANGE_OR_REPORT_PERIOD" {
			n, ok := precise.Number(value["period_seconds"])
			if !ok || !n.IsInt() || n.Sign() <= 0 {
				add(path+"/period_seconds", "range", "periodic reporting requires a positive integer")
			}
		}
	}
	check("report_strategy", p.ReportStrategy)
	known := map[string]bool{}
	for i, point := range p.Datapoints {
		key, _ := point["key"].(string)
		known[key] = true
		if raw, ok := point["report_strategy"]; ok {
			value, ok := raw.(map[string]any)
			if !ok {
				add(fmt.Sprintf("datapoints/%d/report_strategy", i), "type", "report strategy must be an object")
			} else {
				check(fmt.Sprintf("datapoints/%d/report_strategy", i), value)
			}
		}
	}
	seen := map[string]bool{}
	for i, override := range p.StrategyOverrides {
		key, _ := override["key"].(string)
		path := fmt.Sprintf("strategy_overrides/%d", i)
		if !known[key] || seen[key] {
			add(path+"/key", "identity", "strategy override must name a unique configured datapoint")
		}
		seen[key] = true
		value, ok := override["strategy"].(map[string]any)
		if !ok {
			add(path+"/strategy", "type", "strategy must be an object")
		} else {
			check(path+"/strategy", value)
		}
	}
}
func actionFields(protocol string) []Field {
	fields := []Field{field("type", "动作类型", "string", "", "现场连接器执行的动作类型", true, nil, nil), field("param", "命令参数", "string", "", "从命令参数取得写入值的字段", false, nil, nil), field("value", "固定值", "string", "", "未提供命令参数时的固定写入值", false, nil, nil), field("data_type", "数值类型", "string", "", "执行编码时的数据类型", false, nil, nil, "bool", "int16", "uint16", "int32", "uint32", "int64", "uint64", "float32", "float64", "double", "string")}
	switch protocol {
	case "modbus_tcp":
		fields[0].Enum = []string{"write_single_coil", "write_coils", "write_single_register", "write_registers"}
		fields = append(fields, field("address", "写入地址", "integer", "", "从零开始的寄存器地址", true, bound(0), bound(65535)), field("quantity", "写入数量", "integer", "", "多寄存器写入数量", false, bound(1), bound(123)), field("byte_order", "字节序", "string", "", "寄存器内字节顺序", false, nil, nil, "big", "little"), field("word_order", "字序", "string", "", "多寄存器排列顺序", false, nil, nil, "big", "little"))
	case "mqtt_device":
		fields[0].Required = false
		fields = append(fields, field("topic", "命令主题", "string", "", "发布命令的 MQTT 主题", false, nil, nil), field("template", "消息模板", "string", "", "现场连接器使用的命令消息模板", false, nil, nil))
	case "opcua":
		fields[0].Enum = []string{"write", "call"}
		fields = append(fields, field("node_id", "目标节点", "string", "", "OPC-UA 写入或方法对象的节点", true, nil, nil), field("method_id", "方法标识", "string", "", "方法调用需要对应的 method_id", false, nil, nil))
	}
	return fields
}
func checkActionMappings(protocol string, converter map[string]any, add func(string, string, string)) {
	raw, exists := converter["action_mappings"]
	if !exists {
		return
	}
	mappings, ok := raw.(map[string]any)
	if !ok {
		add("converter/action_mappings", "type", "action mappings must be an object")
		return
	}
	if len(mappings) > 100 {
		add("converter/action_mappings", "budget", "at most 100 action mappings are supported")
	}
	for action, raw := range mappings {
		path := "converter/action_mappings/" + action
		if strings.TrimSpace(action) == "" {
			add(path, "identity", "action name is required")
		}
		mapping, ok := raw.(map[string]any)
		if !ok {
			add(path, "type", "action mapping must be an object")
			continue
		}
		checkFields(path, mapping, actionFields(protocol), add)
		typ, _ := mapping["type"].(string)
		if protocol == "opcua" && typ == "call" {
			method, _ := mapping["method_id"].(string)
			if method == "" {
				add(path+"/method_id", "required", "method call requires a method_id")
			}
		}
	}
}

package model

type ParameterMetadata struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     any      `json:"default,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Minimum     *int64   `json:"minimum,omitempty"`
	Maximum     *int64   `json:"maximum,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Description string   `json:"description"`
}

type NodeMetadata struct {
	Type            string              `json:"type"`
	Label           string              `json:"label"`
	Kinds           []string            `json:"kinds"`
	Inputs          []Port              `json:"inputs"`
	Outputs         []Port              `json:"outputs"`
	Parameters      []ParameterMetadata `json:"parameters"`
	ParameterSchema map[string]any      `json:"parameter_schema"`
	Stateful        bool                `json:"stateful"`
	Description     string              `json:"description"`
}

// NodeCatalog supplies validation, contracts, forms and tool descriptions. An
// empty connection port names the default value port.
func NodeCatalog() []NodeMetadata {
	all := []string{"analysis", "alarm", "strategy"}
	value := []Port{{Name: "value", Type: "any"}}
	boolean := []Port{{Name: "value", Type: "boolean"}, {Name: "true", Type: "boolean"}, {Name: "false", Type: "boolean"}}
	minimum, maximum := int64(1), int64(86400000)
	comparison := []ParameterMetadata{
		{Key: "operator", Label: "比较运算符", Type: "string", Default: ">=", Enum: []string{"==", "!=", ">", ">=", "<", "<="}, Description: "按照指定运算符比较输入值与判断值"},
		{Key: "value", Label: "判断值", Type: "json", Required: true, Description: "等值比较保留 JSON 值类型，大小比较使用数值"},
	}
	catalog := []NodeMetadata{
		{Type: "input", Label: "数据输入", Kinds: all, Inputs: []Port{}, Outputs: value, Parameters: []ParameterMetadata{{Key: "key", Label: "输入字段", Type: "string", Description: "留空时使用当前观测，指定字段时使用同设备在观测时刻之前的最新值"}}, Description: "从运行环境提供的观测读取数值"},
		{Type: "aggregate", Label: "窗口聚合", Kinds: []string{"analysis", "alarm"}, Inputs: value, Outputs: []Port{{Name: "value", Type: "number"}}, Parameters: []ParameterMetadata{{Key: "function", Label: "聚合函数", Type: "string", Required: true, Enum: []string{"max", "min", "sum", "count", "avg"}, Description: "对窗口中质量为 GOOD 的数值进行聚合"}}, Description: "按选择器窗口计算聚合结果"},
		{Type: "expression", Label: "计算表达式", Kinds: all, Inputs: value, Outputs: []Port{{Name: "value", Type: "any"}, {Name: "error", Type: "string"}}, Parameters: []ParameterMetadata{{Key: "code", Label: "表达式", Type: "string", Required: true, Description: "允许算术、比较、逻辑判断以及 min、max、abs、round、choose，最多 4096 字节"}}, Description: "执行发布时编译的有限表达式"},
		{Type: "threshold", Label: "阈值判断", Kinds: []string{"alarm", "strategy"}, Inputs: value, Outputs: boolean, Parameters: comparison, Description: "比较有效观测与判断值"},
		{Type: "hysteresis", Label: "回差判断", Kinds: []string{"alarm", "strategy"}, Inputs: value, Outputs: boolean, Stateful: true, Parameters: []ParameterMetadata{{Key: "high", Label: "上限", Type: "number", Required: true, Description: "达到上限时切换状态"}, {Key: "low", Label: "下限", Type: "number", Required: true, Description: "达到下限时恢复状态，下限不得大于上限"}, {Key: "direction", Label: "激活方向", Type: "string", Default: "above", Enum: []string{"above", "below"}, Description: "above 在上限激活，below 在下限激活"}}, Description: "用上限和下限维持回差状态"},
		{Type: "debounce", Label: "延时判断", Kinds: []string{"alarm", "strategy"}, Inputs: value, Outputs: boolean, Stateful: true, Parameters: []ParameterMetadata{{Key: "duration_ms", Label: "等待时间", Type: "integer", Required: true, Minimum: &minimum, Maximum: &maximum, Unit: "ms", Description: "候选状态持续达到指定时间后切换"}, {Key: "mode", Label: "延时模式", Type: "string", Default: "both", Enum: []string{"both", "activation"}, Description: "both 对两个方向延时，activation 在取消激活时立即恢复"}}, Description: "按照显式逻辑时刻推进延时状态"},
		{Type: "counter", Label: "事件计数", Kinds: []string{"analysis"}, Inputs: value, Outputs: []Port{{Name: "value", Type: "integer"}}, Stateful: true, Parameters: []ParameterMetadata{{Key: "mode", Label: "计数方式", Type: "string", Default: "count", Enum: []string{"count", "delta", "rising"}, Description: "count 每条有效观测加一，delta 累加输入整数，rising 统计布尔值上升沿"}}, Description: "保留累计整数与前一输入值"},
		{Type: "output", Label: "持久输出", Kinds: []string{"analysis"}, Inputs: value, Outputs: value, Parameters: []ParameterMetadata{}, Description: "将节点值提供给定义的输出字段"},
		{Type: "alarm", Label: "告警状态", Kinds: []string{"alarm"}, Inputs: value, Outputs: boolean, Parameters: []ParameterMetadata{{Key: "severity", Label: "告警级别", Type: "string", Default: "WARNING", Description: "告警记录使用的级别"}}, Description: "输出告警激活或恢复判断"},
		{Type: "condition", Label: "条件判断", Kinds: []string{"strategy"}, Inputs: value, Outputs: boolean, Parameters: comparison, Description: "判断预案条件是否满足"},
		{Type: "action", Label: "预案动作", Kinds: []string{"strategy"}, Inputs: value, Outputs: boolean, Stateful: true, Parameters: []ParameterMetadata{}, Description: "将激活上升沿交给执行层处理"},
		{Type: "branch", Label: "条件分支", Kinds: all, Inputs: value, Outputs: boolean, Parameters: comparison, Description: "通过 true 或 false 端口选择后续计算"},
	}
	for i := range catalog {
		catalog[i].ParameterSchema = NodeParameterSchema(catalog[i])
	}
	return catalog
}

// NodeParameterSchema is generated from the same metadata that compilation
// consumes. Authoring extension keys remain valid for existing clients.
func NodeParameterSchema(metadata NodeMetadata) map[string]any {
	properties, required := map[string]any{}, []string{}
	for _, field := range metadata.Parameters {
		property := map[string]any{"description": field.Description, "title": field.Label}
		if field.Type != "json" {
			property["type"] = field.Type
		}
		if field.Required {
			required = append(required, field.Key)
			if field.Type == "string" {
				property["minLength"] = 1
			}
		}
		if field.Default != nil {
			property["default"] = field.Default
		}
		if len(field.Enum) > 0 {
			values := append([]string{}, field.Enum...)
			if field.Default != nil {
				values = append(values, "")
			}
			property["enum"] = values
		}
		if field.Minimum != nil {
			property["minimum"] = *field.Minimum
		}
		if field.Maximum != nil {
			property["maximum"] = *field.Maximum
		}
		if field.Type == "integer" && field.Minimum == nil {
			property["minimum"] = int64(-9223372036854775807 - 1)
		}
		if field.Type == "integer" && field.Maximum == nil {
			property["maximum"] = int64(9223372036854775807)
		}
		if field.Unit != "" {
			property["x-unit"] = field.Unit
		}
		properties[field.Key] = property
	}
	schema := map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": true}
	if len(required) == 0 {
		schema["type"] = []string{"object", "null"}
	}
	return schema
}

func NodeKinds() map[string][]string {
	out := map[string][]string{}
	for _, node := range NodeCatalog() {
		out[node.Type] = append([]string{}, node.Kinds...)
	}
	return out
}

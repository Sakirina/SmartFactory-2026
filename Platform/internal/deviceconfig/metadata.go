// Package deviceconfig describes and validates the existing DataTransfer wire
// configuration without changing connector execution or credential storage.
package deviceconfig

import "competition2026/product/platform/pkg/model"

type Field struct {
	model.ParameterMetadata
	Sensitive bool `json:"sensitive,omitempty"`
}
type Section struct {
	Path               string  `json:"path"`
	Keyed              bool    `json:"keyed,omitempty"`
	Repeated           bool    `json:"repeated"`
	Fields             []Field `json:"fields"`
	PreserveExtensions bool    `json:"preserve_extensions"`
}
type ProtocolMetadata struct {
	Protocol    string    `json:"protocol"`
	Aliases     []string  `json:"aliases"`
	Label       string    `json:"label"`
	Version     int       `json:"version"`
	Sections    []Section `json:"sections"`
	Description string    `json:"description"`
}

func field(key, label, typ, unit, description string, required bool, min, max *int64, values ...string) Field {
	return Field{ParameterMetadata: model.ParameterMetadata{Key: key, Label: label, Type: typ, Unit: unit, Description: description, Required: required, Minimum: min, Maximum: max, Enum: values}}
}
func bound(n int64) *int64 { return &n }
func Protocol(protocol string) string {
	switch protocol {
	case "modbus", "modbus_tcp":
		return "modbus_tcp"
	case "mqtt", "mqtt_device":
		return "mqtt_device"
	case "opcua", "opc-ua":
		return "opcua"
	}
	return ""
}

func Catalog() []ProtocolMetadata {
	result := []ProtocolMetadata{}
	for _, p := range []struct {
		id, label string
		aliases   []string
	}{{"modbus_tcp", "Modbus TCP", []string{"modbus"}}, {"mqtt_device", "MQTT", []string{"mqtt"}}, {"opcua", "OPC-UA", []string{"opc-ua"}}} {
		connection := []Field{field("timeout_millis", "连接超时", "integer", "ms", "连接器连接与请求的超时时间", false, bound(1), bound(3600000)), field("username", "用户名", "string", "", "沿用连接器已有身份配置", false, nil, nil), field("password", "密码", "string", "", "沿用连接器已有凭据保存方式", false, nil, nil)}
		connection[2].Sensitive = true
		polling := []Field{field("interval_millis", "采样周期", "integer", "ms", "轮询模式的采样间隔", false, bound(1), bound(86400000)), field("timeout_millis", "采样超时", "integer", "ms", "单次采样的超时时间", false, bound(1), bound(3600000))}
		datapoints := []Field{field("key", "数据字段", "string", "", "观测数据中使用的字段标识", true, nil, nil), field("data_type", "数据类型", "string", "", "数据转换时使用的标量类型", false, nil, nil, "bool", "int16", "uint16", "int32", "uint32", "int64", "uint64", "float32", "float64", "double", "string"), field("scale", "比例系数", "number", "", "转换结果乘以该系数，省略时为一", false, nil, nil), field("offset", "偏移量", "number", "", "转换结果在比例计算后增加该数值", false, nil, nil), field("unit", "单位", "string", "", "与观测记录一起保存的单位", false, nil, nil), field("quality", "质量", "string", "", "固定质量标记，省略时采用协议报告", false, nil, nil, "GOOD", "BAD", "UNCERTAIN")}
		address := []Field{}
		switch p.id {
		case "modbus_tcp":
			connection = append(connection, field("host", "服务器地址", "string", "", "Modbus TCP 主机名或地址", true, nil, nil), field("port", "服务端口", "integer", "", "Modbus TCP 服务端口，常用值为502", false, bound(1), bound(65535)), field("unit_id", "从站地址", "integer", "", "连接器默认从站地址", false, bound(0), bound(255)))
			address = append(address, field("unit_id", "设备从站地址", "integer", "", "覆盖连接器默认从站地址", false, bound(0), bound(255)))
			datapoints = append(datapoints, field("register_type", "寄存器类型", "string", "", "协议读取区域", true, nil, nil, "coil", "discrete_input", "holding_register", "input_register"), field("address", "寄存器地址", "integer", "", "从零开始的寄存器地址", true, bound(0), bound(65535)), field("quantity", "寄存器数量", "integer", "", "省略时由数据类型决定读取宽度", false, bound(1), bound(2000)), field("byte_order", "字节序", "string", "", "每个寄存器内部的字节顺序", false, nil, nil, "big", "little"), field("word_order", "字序", "string", "", "多个寄存器的排列顺序", false, nil, nil, "big", "little"), field("bit_offset", "位偏移", "integer", "bit", "需要截取位段时的起始位置", false, bound(0), bound(63)), field("bit_length", "位长度", "integer", "bit", "零表示使用完整数值", false, bound(0), bound(64)))
			datapoints[1].Enum = []string{"bool", "int16", "uint16", "int32", "uint32", "int64", "uint64", "float32", "float64", "double"}
		case "mqtt_device":
			connection = append(connection, field("url", "Broker 地址", "string", "", "MQTT Broker 的完整连接地址", true, nil, nil), field("mqtt_version", "MQTT 版本", "string", "", "省略时使用3.1.1", false, nil, nil, "3.1.1", "5.0"))
			for _, name := range []string{"telemetry_topic", "status_topic", "event_topic", "cmd_response_topic", "command_topic"} {
				connection = append(connection, field(name, name, "string", "", "沿用连接器对应消息的主题配置", false, nil, nil))
			}
			datapoints = append(datapoints, field("source", "消息字段路径", "string", "", "从设备消息中读取数值的字段路径", true, nil, nil))
		case "opcua":
			connection = append(connection, field("url", "服务器地址", "string", "", "以 opc.tcp:// 开始的服务器地址", true, nil, nil), field("security_mode", "安全模式", "string", "", "证书与身份配置需要符合所选模式", false, nil, nil, "None", "Sign", "SignAndEncrypt"), field("security_policy", "安全策略", "string", "", "沿用服务器公布的 OPC-UA 安全策略", false, nil, nil, "None", "Basic128Rsa15", "Basic256", "Basic256Sha256", "Aes128_Sha256_RsaOaep", "Aes256_Sha256_RsaPss"))
			for _, name := range []string{"cert_file", "key_file", "ca_file"} {
				f := field(name, name, "string", "", "沿用现场运行环境中的证书文件引用", false, nil, nil)
				f.Sensitive = name == "key_file"
				connection = append(connection, f)
			}
			polling = append(polling, field("mode", "采样方式", "string", "", "省略时使用订阅方式", false, nil, nil, "subscribe", "poll"), field("publish_interval_millis", "订阅发布间隔", "integer", "ms", "OPC-UA 订阅发布间隔", false, bound(1), bound(86400000)))
			datapoints = append(datapoints, field("node_id", "节点标识", "string", "", "服务器命名空间中的 OPC-UA 节点标识", true, nil, nil))
		}
		sections := []Section{{Path: "connection", Fields: connection, PreserveExtensions: true}, {Path: "polling", Fields: polling, PreserveExtensions: true}, {Path: "address", Fields: address, PreserveExtensions: true}, {Path: "datapoints", Repeated: true, Fields: datapoints, PreserveExtensions: true}, {Path: "report_strategy", Fields: []Field{field("mode", "上报策略", "string", "", "沿用当前消息过滤策略", false, nil, nil, "STRATEGY_UNSPECIFIED", "ON_RECEIVED", "ON_CHANGE", "ON_REPORT_PERIOD", "ON_CHANGE_OR_REPORT_PERIOD"), field("period_seconds", "上报周期", "integer", "s", "按周期上报时必须提供正整数", false, bound(1), bound(86400)), field("deadband", "变化阈值", "number", "", "数值变化达到该阈值时上报", false, bound(0), nil)}, PreserveExtensions: true}}
		sections[len(sections)-1].Fields = reportFields()
		if p.id == "mqtt_device" {
			sections = append(sections, Section{Path: "connection.tls", Fields: []Field{
				field("enabled", "启用 TLS", "boolean", "", "使用 TLS 建立 MQTT 连接", false, nil, nil),
				field("insecure_skip_verify", "跳过服务器证书验证", "boolean", "", "沿用现场配置的证书验证选项", false, nil, nil),
				field("cert_file", "客户端证书", "string", "", "现场运行环境中的证书文件引用", false, nil, nil),
				field("key_file", "客户端私钥", "string", "", "现场运行环境中的私钥文件引用", false, nil, nil),
				field("ca_file", "服务器 CA", "string", "", "现场运行环境中的 CA 文件引用", false, nil, nil),
			}, PreserveExtensions: true})
		}
		sections = append(sections, Section{Path: "converter.action_mappings", Keyed: true, Fields: actionFields(p.id), PreserveExtensions: true})
		result = append(result, ProtocolMetadata{Protocol: p.id, Aliases: p.aliases, Label: p.label, Version: 1, Sections: sections, Description: "基础参数与正式 DataTransfer 配置使用相同字段；扩展参数按原始 JSON 往返保存"})
	}
	return result
}

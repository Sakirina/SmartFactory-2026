package deviceconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/platform/pkg/model"
	"competition2026/product/platform/pkg/precise"
	"google.golang.org/protobuf/encoding/protojson"
)

type Parameters struct {
	Kind              string            `json:"kind" enum:"device,connector" required:"true"`
	ConnectorID       string            `json:"connector_id" required:"true"`
	DeviceID          string            `json:"device_id,omitempty"`
	DeviceName        string            `json:"device_name,omitempty"`
	DeviceType        string            `json:"device_type,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
	Address           map[string]any    `json:"address,omitempty"`
	Datapoints        []map[string]any  `json:"datapoints,omitempty"`
	Connection        map[string]any    `json:"connection,omitempty"`
	Polling           map[string]any    `json:"polling,omitempty"`
	Converter         map[string]any    `json:"converter,omitempty"`
	ReportStrategy    map[string]any    `json:"report_strategy,omitempty"`
	StrategyOverrides []map[string]any  `json:"strategy_overrides,omitempty"`
}
type Issue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Request struct {
	Protocol   string          `json:"protocol" required:"true"`
	DeviceID   string          `json:"device_id,omitempty"`
	Config     json.RawMessage `json:"config,omitempty"`
	Parameters *Parameters     `json:"parameters,omitempty"`
}
type Result struct {
	Protocol   string          `json:"protocol"`
	Config     json.RawMessage `json:"config"`
	Parameters Parameters      `json:"parameters"`
	Valid      bool            `json:"valid"`
	Issues     []Issue         `json:"issues"`
}

func decode(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return d.Decode(v)
}
func mapJSON(data []byte) (map[string]any, error) {
	result := map[string]any{}
	if len(data) > 0 {
		if err := decode(data, &result); err != nil {
			return nil, err
		}
		if result == nil {
			return nil, errors.New("JSON object is required")
		}
	}
	return result, nil
}

func Validate(request Request) (Result, error) {
	result := Result{Protocol: Protocol(request.Protocol), Issues: []Issue{}}
	if result.Protocol == "" {
		return result, errors.New("supported protocol is modbus_tcp, mqtt_device or opcua")
	}
	var update dt.DeviceConfigUpdate
	if len(request.Config) > 0 {
		if err := protojson.Unmarshal(request.Config, &update); err != nil {
			return result, fmt.Errorf("device configuration envelope: %w", err)
		}
	}
	p := Parameters{}
	if device := update.GetDeviceConfig(); device != nil {
		p = Parameters{Kind: "device", ConnectorID: device.ConnectorId, DeviceID: device.DeviceId, DeviceName: device.DeviceName, DeviceType: device.DeviceType, Tags: device.Tags}
		var err error
		p.Address, err = mapJSON(device.Address)
		if err != nil {
			return result, fmt.Errorf("address: %w", err)
		}
		if len(device.Datapoints) > 0 {
			if err = decode(device.Datapoints, &p.Datapoints); err != nil {
				return result, fmt.Errorf("datapoints: %w", err)
			}
		}
		for _, override := range device.StrategyOverrides {
			p.StrategyOverrides = append(p.StrategyOverrides, map[string]any{"key": override.Key, "strategy": reportParameters(override.Strategy)})
		}
	} else if connector := update.GetConnectorConfig(); connector != nil {
		if Protocol(connector.Protocol) != result.Protocol {
			return result, errors.New("connector protocol differs from selected protocol")
		}
		p = Parameters{Kind: "connector", ConnectorID: connector.ConnectorId, Tags: connector.DefaultTags}
		p.ReportStrategy = reportParameters(connector.ReportStrategy)
		var err error
		p.Connection, err = mapJSON(connector.Connection)
		if err != nil {
			return result, err
		}
		p.Polling, err = mapJSON(connector.Polling)
		if err != nil {
			return result, err
		}
		p.Converter, err = mapJSON(connector.Converter)
		if err != nil {
			return result, err
		}
	} else if len(request.Config) > 0 {
		return result, errors.New("a device or connector configuration payload is required")
	}
	if request.Parameters != nil {
		edited := request.Parameters
		if p.Kind != "" && p.Kind != edited.Kind {
			return result, errors.New("configuration kind cannot change")
		}
		p.Kind = edited.Kind
		p.ConnectorID = edited.ConnectorID
		p.DeviceID = edited.DeviceID
		p.DeviceName = edited.DeviceName
		p.DeviceType = edited.DeviceType
		if edited.Tags != nil {
			p.Tags = edited.Tags
		}
		p.Address = merge(p.Address, edited.Address)
		p.Connection = merge(p.Connection, edited.Connection)
		p.Polling = merge(p.Polling, edited.Polling)
		p.Converter = merge(p.Converter, edited.Converter)
		p.ReportStrategy = merge(p.ReportStrategy, edited.ReportStrategy)
		if edited.StrategyOverrides != nil {
			p.StrategyOverrides = edited.StrategyOverrides
		}
		if edited.Datapoints != nil {
			old := map[string]map[string]any{}
			for _, dp := range p.Datapoints {
				key, _ := dp["key"].(string)
				old[key] = dp
			}
			p.Datapoints = []map[string]any{}
			for _, dp := range edited.Datapoints {
				key, _ := dp["key"].(string)
				p.Datapoints = append(p.Datapoints, merge(old[key], dp))
			}
		}
	}
	result.Parameters = p
	add := func(path, code, message string) {
		result.Issues = append(result.Issues, Issue{Path: path, Code: code, Message: message})
	}
	if p.Kind == "connector" {
		normalizeTLS(p.Connection, add)
	}
	if strings.TrimSpace(p.ConnectorID) == "" {
		add("connector_id", "required", "connector_id is required")
	}
	if p.Kind == "device" {
		if p.DeviceID == "" {
			add("device_id", "required", "device_id is required")
		}
		if request.DeviceID != "" && p.DeviceID != request.DeviceID {
			add("device_id", "identity_mismatch", "device_id differs from the entity")
		}
		if len(p.Datapoints) == 0 {
			add("datapoints", "required", "at least one datapoint is required")
		}
		if update.Action != dt.DeviceConfigUpdate_ACTION_UNSPECIFIED && update.Action != dt.DeviceConfigUpdate_ADD_DEVICE && update.Action != dt.DeviceConfigUpdate_UPDATE_DEVICE {
			add("action", "invalid", "device configuration supports ADD_DEVICE or UPDATE_DEVICE")
		}
	} else if p.Kind != "connector" {
		add("kind", "invalid", "kind must be device or connector")
	}
	metadata := ProtocolMetadata{}
	for _, entry := range Catalog() {
		if entry.Protocol == result.Protocol {
			metadata = entry
		}
	}
	seen := map[string]bool{}
	for _, section := range metadata.Sections {
		if p.Kind == "device" && (strings.HasPrefix(section.Path, "connection") || section.Path == "polling") {
			continue
		}
		if p.Kind == "connector" && (section.Path == "address" || section.Path == "datapoints") {
			continue
		}
		if section.Path == "datapoints" {
			for i, dp := range p.Datapoints {
				path := "datapoints/" + strconv.Itoa(i)
				checkFields(path, dp, section.Fields, add)
				key, _ := dp["key"].(string)
				if seen[key] {
					add(path+"/key", "duplicate", "datapoint keys must be unique")
				}
				seen[key] = true
				crossDatapoint(path, result.Protocol, dp, add)
			}
		} else {
			var values map[string]any
			switch section.Path {
			case "connection":
				values = p.Connection
			case "connection.tls":
				if raw, exists := p.Connection["tls"]; exists {
					var ok bool
					values, ok = raw.(map[string]any)
					if !ok {
						add("connection/tls", "type", "TLS configuration must be an object")
						continue
					}
				}
			case "polling":
				values = p.Polling
			case "address":
				values = p.Address
			default:
				continue
			}
			checkFields(section.Path, values, section.Fields, add)
		}
	}
	if p.Kind == "connector" {
		crossConnection(result.Protocol, p.Connection, add)
		checkActionMappings(result.Protocol, p.Converter, add)
	}
	checkReportParameters(p, add)
	if p.Kind == "device" {
		device := update.GetDeviceConfig()
		if device == nil {
			device = &dt.DeviceConfigPayload{}
			update.Config = &dt.DeviceConfigUpdate_DeviceConfig{DeviceConfig: device}
			update.Action = dt.DeviceConfigUpdate_UPDATE_DEVICE
		}
		device.DeviceId = p.DeviceID
		device.DeviceName = p.DeviceName
		device.DeviceType = p.DeviceType
		device.ConnectorId = p.ConnectorID
		device.Tags = p.Tags
		device.StrategyOverrides = nil
		for _, override := range p.StrategyOverrides {
			key, _ := override["key"].(string)
			strategy, _ := override["strategy"].(map[string]any)
			value, e := reportProto(strategy)
			if e != nil {
				return result, e
			}
			device.StrategyOverrides = append(device.StrategyOverrides, &dt.DatapointStrategyOverride{Key: key, Strategy: value})
		}
		var err error
		device.Address, err = json.Marshal(p.Address)
		if err != nil {
			return result, err
		}
		device.Datapoints, err = json.Marshal(p.Datapoints)
		if err != nil {
			return result, err
		}
	} else if p.Kind == "connector" {
		connector := update.GetConnectorConfig()
		if connector == nil {
			connector = &dt.ConnectorConfigPayload{}
			update.Config = &dt.DeviceConfigUpdate_ConnectorConfig{ConnectorConfig: connector}
			update.Action = dt.DeviceConfigUpdate_UPDATE_CONNECTOR
		}
		connector.ConnectorId = p.ConnectorID
		connector.Protocol = result.Protocol
		connector.DefaultTags = p.Tags
		var reportErr error
		connector.ReportStrategy, reportErr = reportProto(p.ReportStrategy)
		if reportErr != nil {
			return result, reportErr
		}
		var err error
		connector.Connection, err = json.Marshal(p.Connection)
		if err != nil {
			return result, err
		}
		connector.Polling, err = json.Marshal(p.Polling)
		if err != nil {
			return result, err
		}
		connector.Converter, err = json.Marshal(p.Converter)
		if err != nil {
			return result, err
		}
	}
	raw, err := protojson.Marshal(&update)
	if err != nil {
		return result, err
	}
	result.Config = raw
	result.Valid = len(result.Issues) == 0
	return result, nil
}

func merge(original, edited map[string]any) map[string]any {
	result := map[string]any{}
	for k, v := range original {
		result[k] = v
	}
	for k, v := range edited {
		if child, ok := v.(map[string]any); ok {
			old, _ := result[k].(map[string]any)
			result[k] = merge(old, child)
		} else {
			result[k] = v
		}
	}
	return result
}
func checkFields(path string, values map[string]any, fields []Field, add func(string, string, string)) {
	for _, f := range fields {
		v, exists := values[f.Key]
		target := path + "/" + f.Key
		if !exists || v == nil {
			if f.Required {
				add(target, "required", f.Key+" is required")
			}
			continue
		}
		switch f.Type {
		case "boolean":
			if _, ok := v.(bool); !ok {
				add(target, "type", "a boolean is required")
			}
		case "string":
			str, ok := v.(string)
			if !ok {
				add(target, "type", "a string is required")
				continue
			}
			if f.Required && strings.TrimSpace(str) == "" {
				add(target, "required", "a nonempty string is required")
			}
			if len(f.Enum) > 0 && str != "" {
				found := false
				for _, option := range f.Enum {
					found = found || option == str
				}
				if !found {
					add(target, "enum", "value is outside the declared enumeration")
				}
			}
		case "integer", "number":
			n, ok := precise.Number(v)
			if !ok || f.Type == "integer" && !n.IsInt() {
				add(target, "type", f.Type+" is required")
				continue
			}
			if f.Minimum != nil {
				min, _ := precise.Number(*f.Minimum)
				if n.Cmp(min) < 0 {
					add(target, "minimum", "value is below the allowed minimum")
				}
			}
			if f.Maximum != nil {
				max, _ := precise.Number(*f.Maximum)
				if n.Cmp(max) > 0 {
					add(target, "maximum", "value exceeds the allowed maximum")
				}
			}
		}
	}
}
func crossDatapoint(path, protocol string, dp map[string]any, add func(string, string, string)) {
	if strategy, ok := dp["report_strategy"].(map[string]any); ok {
		for _, m := range Catalog() {
			if m.Protocol != protocol {
				continue
			}
			for _, s := range m.Sections {
				if s.Path == "report_strategy" {
					checkFields(path+"/report_strategy", strategy, s.Fields, add)
				}
			}
		}
		mode, _ := strategy["mode"].(string)
		if mode == "ON_REPORT_PERIOD" || mode == "ON_CHANGE_OR_REPORT_PERIOD" {
			if n, ok := precise.Number(strategy["period_seconds"]); !ok || n.Sign() <= 0 {
				add(path+"/report_strategy/period_seconds", "required", "periodic reporting requires a positive period")
			}
		}
	}
	if protocol != "modbus_tcp" {
		return
	}
	typ, _ := dp["data_type"].(string)
	width := int64(16)
	switch typ {
	case "int32", "uint32", "float32":
		width = 32
	case "int64", "uint64", "float64", "double":
		width = 64
	}
	reg, _ := dp["register_type"].(string)
	if (reg == "coil" || reg == "discrete_input") && typ != "" && typ != "bool" {
		add(path+"/data_type", "incompatible", "bit registers require bool")
	}
	quantity := width / 16
	if q, ok := precise.Number(dp["quantity"]); ok && q.IsInt() && q.Num().IsInt64() {
		quantity = q.Num().Int64()
	}
	if reg == "holding_register" || reg == "input_register" {
		if quantity < width/16 || quantity > 125 {
			add(path+"/quantity", "register_width", "register quantity must cover the data width and remain at most 125")
		}
	}
	if a, ok := precise.Number(dp["address"]); ok && a.IsInt() && a.Num().IsInt64() && a.Num().Int64()+quantity > 65536 {
		add(path+"/address", "register_range", "register range exceeds 65535")
	}
	bit, ok := precise.Number(dp["bit_length"])
	if ok && bit.Sign() > 0 {
		offset, _ := precise.Number(dp["bit_offset"])
		value := int64(0)
		if offset != nil && offset.IsInt() && offset.Num().IsInt64() {
			value = offset.Num().Int64()
		}
		if bit.IsInt() && bit.Num().IsInt64() && value+bit.Num().Int64() > width {
			add(path+"/bit_length", "bit_range", "bit selection exceeds the data width")
		}
	}
}
func crossConnection(protocol string, c map[string]any, add func(string, string, string)) {
	if tls, ok := c["tls"].(map[string]any); ok {
		cert, _ := tls["cert_file"].(string)
		key, _ := tls["key_file"].(string)
		if (cert == "") != (key == "") {
			add("connection/tls", "certificate_pair", "client certificate and key references must be provided together")
		}
	}
	if protocol == "mqtt_device" || protocol == "opcua" {
		raw, _ := c["url"].(string)
		u, e := url.Parse(raw)
		valid := e == nil && u.Host != ""
		if valid && protocol == "opcua" {
			valid = u.Scheme == "opc.tcp"
		}
		if valid && protocol == "mqtt_device" {
			valid = u.Scheme == "tcp" || u.Scheme == "ssl" || u.Scheme == "tls" || u.Scheme == "mqtt" || u.Scheme == "mqtts" || u.Scheme == "ws" || u.Scheme == "wss"
		}
		if !valid {
			add("connection/url", "url", "connection URL does not match the protocol")
		}
	}
	if protocol != "opcua" {
		return
	}
	mode, _ := c["security_mode"].(string)
	policy, _ := c["security_policy"].(string)
	if mode == "" {
		mode = "None"
	}
	if policy == "" {
		policy = "None"
	}
	if mode != "None" {
		for _, key := range []string{"cert_file", "key_file", "ca_file"} {
			value, _ := c[key].(string)
			if value == "" {
				add("connection/"+key, "required", "signed OPC-UA connections require certificate file references")
			}
		}
		if policy == "None" {
			add("connection/security_policy", "security", "signed connections require an encryption policy")
		}
	} else if policy != "None" {
		add("connection/security_policy", "security", "None mode requires None policy")
	}
	if username, _ := c["username"].(string); username != "" && mode != "SignAndEncrypt" {
		add("connection/security_mode", "security", "password authentication requires SignAndEncrypt")
	}
}

func ValidateEntity(entity model.Entity) error {
	if entity.Kind != "device" || len(entity.Config) == 0 {
		return nil
	}
	result, err := Validate(Request{Protocol: entity.Protocol, DeviceID: entity.ID, Config: entity.Config})
	if err != nil {
		return err
	}
	if !result.Valid {
		return fmt.Errorf("device configuration %s: %s", result.Issues[0].Path, result.Issues[0].Message)
	}
	if result.Parameters.Kind != "device" {
		return errors.New("device entities require a device payload")
	}
	return nil
}

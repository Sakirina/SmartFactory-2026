package deviceconfig

import (
	"encoding/json"
	"testing"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestConfigurationMetadataRoundTripAndValidation(t *testing.T) {
	if len(Catalog()) != 3 {
		t.Fatal("protocol catalogue")
	}
	for _, protocol := range []string{"modbus_tcp", "mqtt_device", "opcua"} {
		t.Run(protocol, func(t *testing.T) {
			connection := map[string]any{"extension": map[string]any{"serial": json.Number("9007199254740993")}}
			point := map[string]any{"key": "value", "extension": map[string]any{"vendor": "retained"}}
			switch protocol {
			case "modbus_tcp":
				connection["host"] = "127.0.0.1"
				point["register_type"] = "holding_register"
				point["address"] = 2
				point["data_type"] = "uint16"
			case "mqtt_device":
				connection["url"] = "tls://127.0.0.1:8883"
				connection["tls"] = map[string]any{"enabled": true, "insecure_skip_verify": false, "cert_file": "client.crt", "key_file": "client.key", "ca_file": "ca.crt"}
				point["source"] = "value"
			case "opcua":
				connection["url"] = "opc.tcp://127.0.0.1:4840"
				point["node_id"] = "ns=2;s=value"
			}
			c, e := Validate(Request{Protocol: protocol, Parameters: &Parameters{Kind: "connector", ConnectorID: "test", Connection: connection, ReportStrategy: map[string]any{"mode": "ON_REPORT_PERIOD", "period_seconds": 7}}})
			if e != nil || !c.Valid {
				t.Fatal(c.Issues, e)
			}
			var wire dt.DeviceConfigUpdate
			if e = protojson.Unmarshal(c.Config, &wire); e != nil || wire.GetConnectorConfig().GetReportStrategy().GetPeriodSeconds() != 7 {
				t.Fatal(e)
			}
			revised, e := Validate(Request{Protocol: protocol, Config: c.Config, Parameters: &Parameters{Kind: "connector", ConnectorID: "test", Polling: map[string]any{"interval_millis": 900}}})
			if e != nil || !revised.Valid || revised.Parameters.Connection["extension"].(map[string]any)["serial"].(json.Number).String() != "9007199254740993" {
				t.Fatal(revised, e)
			}
			d, e := Validate(Request{Protocol: protocol, DeviceID: "device", Parameters: &Parameters{Kind: "device", ConnectorID: "test", DeviceID: "device", Datapoints: []map[string]any{point}, StrategyOverrides: []map[string]any{{"key": "value", "strategy": map[string]any{"mode": "ON_CHANGE", "deadband": 0.5}}}}})
			if e != nil || !d.Valid {
				t.Fatal(d.Issues, e)
			}
			d, e = Validate(Request{Protocol: protocol, DeviceID: "device", Config: d.Config, Parameters: &Parameters{Kind: "device", ConnectorID: "test", DeviceID: "device", Datapoints: []map[string]any{{"key": "value", "unit": "unit"}}}})
			if e != nil || !d.Valid || d.Parameters.Datapoints[0]["extension"].(map[string]any)["vendor"] != "retained" || len(d.Parameters.StrategyOverrides) != 1 {
				t.Fatal(d, e)
			}
		})
	}
	for _, entry := range []struct {
		name, protocol string
		p              Parameters
	}{
		{"modbus-width", "modbus_tcp", Parameters{Kind: "device", ConnectorID: "c", DeviceID: "d", Datapoints: []map[string]any{{"key": "v", "register_type": "holding_register", "address": 65535, "data_type": "uint64", "quantity": 1}}}},
		{"mqtt-tls-type", "mqtt_device", Parameters{Kind: "connector", ConnectorID: "c", Connection: map[string]any{"url": "tcp://localhost:1883", "tls": map[string]any{"enabled": "yes"}}}},
		{"opcua-password-security", "opcua", Parameters{Kind: "connector", ConnectorID: "c", Connection: map[string]any{"url": "opc.tcp://localhost:4840", "username": "operator", "password": "private"}}},
		{"action-type", "modbus_tcp", Parameters{Kind: "connector", ConnectorID: "c", Connection: map[string]any{"host": "localhost"}, Converter: map[string]any{"action_mappings": map[string]any{"x": map[string]any{"type": "unsupported", "address": 1}}}}},
	} {
		t.Run(entry.name, func(t *testing.T) {
			r, e := Validate(Request{Protocol: entry.protocol, Parameters: &entry.p})
			if e == nil && r.Valid {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	legacy, e := Validate(Request{Protocol: "mqtt_device", Parameters: &Parameters{Kind: "connector", ConnectorID: "legacy", Connection: map[string]any{"url": "tls://localhost:8883", "tls": map[string]any{"Enabled": true, "InsecureSkipVerify": true, "CertFile": "client.crt", "KeyFile": "client.key", "CAFile": "ca.crt"}}}})
	if e != nil || !legacy.Valid || legacy.Parameters.Connection["tls"].(map[string]any)["key_file"] != "client.key" {
		t.Fatal("legacy TLS normalization", legacy, e)
	}
	invalid, e := Validate(Request{Protocol: "mqtt_device", Parameters: &Parameters{Kind: "connector", ConnectorID: "legacy", Connection: map[string]any{"url": "tls://localhost:8883", "tls": map[string]any{"Enabled": "yes"}}}})
	if e == nil && invalid.Valid {
		t.Fatal("legacy TLS alias bypassed metadata validation")
	}
	t.Log("three actual protobuf payloads preserve extensions, large integers and report strategies; range, type, security and action metadata validated")
}

package config

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// UnmarshalJSON accepts the original Go field names in persisted JSON while
// emitting snake_case for the current configuration wire contract.
func (c *TLSConfig) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for old, current := range map[string]string{"Enabled": "enabled", "InsecureSkipVerify": "insecure_skip_verify", "CertFile": "cert_file", "KeyFile": "key_file", "CAFile": "ca_file"} {
		legacy, exists := fields[old]
		if !exists {
			continue
		}
		if canonical, exists := fields[current]; exists {
			var a, b any
			if err := json.Unmarshal(legacy, &a); err != nil {
				return err
			}
			if err := json.Unmarshal(canonical, &b); err != nil {
				return err
			}
			if !reflect.DeepEqual(a, b) {
				return fmt.Errorf("conflicting TLS field names %s and %s", old, current)
			}
		} else {
			fields[current] = legacy
		}
		delete(fields, old)
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	type plainTLS TLSConfig
	return json.Unmarshal(normalized, (*plainTLS)(c))
}

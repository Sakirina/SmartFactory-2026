package deviceconfig

import "reflect"

func normalizeTLS(connection map[string]any, add func(string, string, string)) {
	tls, ok := connection["tls"].(map[string]any)
	if !ok {
		return
	}
	for old, current := range map[string]string{"Enabled": "enabled", "InsecureSkipVerify": "insecure_skip_verify", "CertFile": "cert_file", "KeyFile": "key_file", "CAFile": "ca_file"} {
		v, ok := tls[old]
		if !ok {
			continue
		}
		if next, exists := tls[current]; exists && !reflect.DeepEqual(v, next) {
			add("connection/tls/"+current, "conflict", "legacy and current TLS field names have different values")
		} else {
			tls[current] = v
		}
		delete(tls, old)
	}
}

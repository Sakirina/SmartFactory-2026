package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTLSJSONLegacyNamesAndCanonicalRoundTrip(t *testing.T) {
	want := TLSConfig{Enabled: true, InsecureSkipVerify: true, CertFile: "cert.pem", KeyFile: "key.pem", CAFile: "ca.pem"}
	for _, data := range []string{`{"Enabled":true,"InsecureSkipVerify":true,"CertFile":"cert.pem","KeyFile":"key.pem","CAFile":"ca.pem"}`, `{"enabled":true,"insecure_skip_verify":true,"cert_file":"cert.pem","key_file":"key.pem","ca_file":"ca.pem"}`} {
		var got TLSConfig
		if e := json.Unmarshal([]byte(data), &got); e != nil || !reflect.DeepEqual(got, want) {
			t.Fatal(got, e)
		}
		raw, e := json.Marshal(got)
		if e != nil || strings.Contains(string(raw), "CertFile") || !strings.Contains(string(raw), `"cert_file":"cert.pem"`) {
			t.Fatal(string(raw), e)
		}
		var again TLSConfig
		if e = json.Unmarshal(raw, &again); e != nil || again != want {
			t.Fatal(again, e)
		}
	}
	var invalid TLSConfig
	if e := json.Unmarshal([]byte(`{"cert_file":"new","CertFile":"old"}`), &invalid); e == nil {
		t.Fatal("conflicting aliases accepted")
	}
}

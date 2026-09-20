package thingsboard

import (
	"competition2026/product/platform/pkg/model"
	"encoding/json"
	"testing"
)

func TestNativeAbsentReadingsAndUnsignedIntegers(t *testing.T) {
	fields := telemetryFields(model.Observation{ID: "bad-reading", Key: "temperature", Quality: "BAD", QualityReason: "BadSensorFailure", Value: nil})
	if _, ok := fields["temperature"]; ok {
		t.Fatal("native scalar null would reject the whole batch")
	}
	if fields["temperature__present"] != false || fields["temperature__quality_reason"] != "BadSensorFailure" || fields["temperature__observation_id"] != "bad-reading" {
		t.Fatal(fields)
	}
	fields = telemetryFields(model.Observation{Key: "counter", Quality: "GOOD", Value: json.Number("18446744073709551615")})
	if fields["counter"] != "18446744073709551615" || fields["counter__encoding"] != "integer_string" || fields["sf_good.counter"] != nil {
		t.Fatal(fields)
	}
	fields = telemetryFields(model.Observation{Key: "counter", Quality: "GOOD", Value: json.Number("9007199254740993")})
	if fields["counter"] != json.Number("9007199254740993") || fields["sf_good.counter"] != fields["counter"] {
		t.Fatal(fields)
	}
}

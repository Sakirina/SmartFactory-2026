package mqttdevice

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"fmt"
	"github.com/tidwall/gjson"
	"math"
	"strconv"
	"testing"
)

func TestThousandJSONConversionsPreserveIntegersAndQuality(t *testing.T) {
	c := NewConnector()
	scale := 0.1
	for i := 0; i < 1000; i++ {
		want := int64(9007199254740993) + int64(i)
		dp := config.DatapointConfig{Key: "count", DataType: "int64"}
		value, e := jsonValue(gjson.Parse(strconv.FormatInt(want, 10)), dp)
		if e != nil || value.GetIntValue() != want {
			t.Fatalf("sample %d lost precision: %v %v", i, value, e)
		}
		dp = config.DatapointConfig{Key: "temperature", Source: "temperature", DataType: "float64", Scale: &scale, Unit: "C"}
		msg, e := c.telemetryMessage(config.ConnectorConfig{}, config.DeviceConfig{DeviceID: "sensor", Datapoints: []config.DatapointConfig{dp}}, []byte(fmt.Sprintf(`{"message_id":"sample-%d","timestamp":1700000000000,"temperature":%d}`, i, i)))
		if e != nil {
			t.Fatal(e)
		}
		p := msg.GetTelemetry().Datapoints[0]
		if p.Quality != dt.DataQuality_GOOD || p.Unit != "C" || p.TimeSource != "device" || p.Timestamp != 1700000000000 || math.Abs(p.Value.GetDoubleValue()-float64(i)*scale) > 1e-6 {
			t.Fatalf("metadata/conversion sample %d: %v", i, p)
		}
	}
	msg, e := c.telemetryMessage(config.ConnectorConfig{}, config.DeviceConfig{Datapoints: []config.DatapointConfig{{Key: "count", Source: "count", DataType: "int64"}}}, []byte(`{"count":"invalid"}`))
	if e != nil || msg.GetTelemetry().Datapoints[0].Quality != dt.DataQuality_BAD {
		t.Fatal("invalid data reported GOOD")
	}
}

package opcua

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"math"
	"testing"
	"time"
)

func TestThousandNativeConversionsAndStatusCodes(t *testing.T) {
	at := time.Unix(1700000000, 0)
	scale := 0.01
	for i := 0; i < 1000; i++ {
		want := int64(9007199254740993) + int64(i)
		p := makeDatapoint(Observation{Value: want, Quality: dt.DataQuality_GOOD, Timestamp: at, TimeSource: "device"}, config.DatapointConfig{Key: "counter", DataType: "int64"})
		if p.Value.GetIntValue() != want || p.Timestamp != at.UnixMilli() {
			t.Fatalf("integer sample %d", i)
		}
		quality := dt.DataQuality_GOOD
		if i%3 == 0 {
			quality = dt.DataQuality_BAD
		}
		if i%3 == 1 {
			quality = dt.DataQuality_UNCERTAIN
		}
		p = makeDatapoint(Observation{Value: float64(i), Quality: quality, Timestamp: at, TimeSource: "device", Reason: "protocol status"}, config.DatapointConfig{Key: "temperature", DataType: "float64", Scale: &scale, Unit: "C"})
		if p.Quality != quality || p.Unit != "C" || p.QualityReason != "protocol status" || math.Abs(p.Value.GetDoubleValue()-float64(i)*scale) > 1e-6 {
			t.Fatalf("status sample %d", i)
		}
	}
}

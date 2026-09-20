package modbus

import (
	"competition2026/product/datatransfer/internal/config"
	"encoding/binary"
	"math"
	"math/rand"
	"strconv"
	"testing"
)

func TestThousandIndependentRegisterConversions(t *testing.T) {
	r := rand.New(rand.NewSource(202606))
	for i := 0; i < 1000; i++ {
		want := r.Uint64()
		raw := make([]byte, 8)
		binary.BigEndian.PutUint64(raw, want)
		words := []uint16{binary.BigEndian.Uint16(raw[:2]), binary.BigEndian.Uint16(raw[2:4]), binary.BigEndian.Uint16(raw[4:6]), binary.BigEndian.Uint16(raw[6:])}
		dp := config.DatapointConfig{DataType: "uint64"}
		if i%2 == 0 {
			dp.WordOrder = "little"
			words[0], words[3] = words[3], words[0]
			words[1], words[2] = words[2], words[1]
		}
		if i%3 == 0 {
			dp.ByteOrder = "little"
			for n, v := range words {
				words[n] = v>>8 | v<<8
			}
		}
		got, e := decodeRegisters(words, dp)
		if e != nil {
			t.Fatal(e)
		}
		text, e := dataValueToString(got)
		if e != nil || text != strconv.FormatUint(want, 10) {
			t.Fatalf("sample %d %s != %d (%v)", i, text, want, e)
		}
		bits := uint16(r.Uint32())
		got, e = decodeRegisters([]uint16{bits}, config.DatapointConfig{DataType: "uint16", BitOffset: 3, BitLength: 5})
		if e != nil || got.GetIntValue() != int64((bits>>3)&31) {
			t.Fatalf("bit extraction sample %d", i)
		}
		scale := 0.01
		f := float32(r.Float64()*1000 - 500)
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, math.Float32bits(f))
		got, e = decodeRegisters([]uint16{binary.BigEndian.Uint16(buf[:2]), binary.BigEndian.Uint16(buf[2:])}, config.DatapointConfig{DataType: "float32", Scale: &scale, Offset: 12})
		expected := float64(f)*scale + 12
		if e != nil || math.Abs(got.GetDoubleValue()-expected) > 1e-6*math.Max(1, math.Abs(expected)) {
			t.Fatalf("float sample %d", i)
		}
	}
}
func TestReverseConversionAndInvalidOrdering(t *testing.T) {
	scale := 0.1
	words, e := encodeMapped("25.5", config.ActionMapping{DataType: "uint32", Scale: &scale, Offset: 0.5, ByteOrder: "little", WordOrder: "little"})
	if e != nil || len(words) != 2 || words[0] != 0xfa00 || words[1] != 0 {
		t.Fatalf("reverse = %v %v", words, e)
	}
	if _, e = decodeRegisters([]uint16{1}, config.DatapointConfig{DataType: "uint16", ByteOrder: "unknown"}); e == nil {
		t.Fatal("invalid byte order accepted")
	}
}

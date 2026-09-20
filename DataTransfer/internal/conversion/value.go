// Package conversion preserves integral protocol values until a configured unit
// conversion requires floating point arithmetic.
package conversion

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
)

func Value(raw any, dp config.DatapointConfig) (*dtv1.DataValue, error) {
	if raw == nil {
		return nil, fmt.Errorf("value is missing")
	}
	kind := strings.ToLower(dp.DataType)
	text := fmt.Sprint(raw)
	if len(text) > 1024 {
		return nil, fmt.Errorf("scalar value exceeds 1024 bytes")
	}
	if kind == "" {
		switch raw.(type) {
		case bool:
			kind = "bool"
		case string:
			kind = "string"
		case float32, float64:
			kind = "float64"
		case json.Number:
			if strings.ContainsAny(text, ".eE") {
				kind = "float64"
			} else {
				kind = "int64"
			}
		default:
			v := reflect.ValueOf(raw)
			if v.Kind() >= reflect.Uint && v.Kind() <= reflect.Uint64 {
				kind = "uint64"
			} else {
				kind = "int64"
			}
		}
	}
	scale := 1.0
	if dp.Scale != nil {
		scale = *dp.Scale
	}
	scaled := func(value float64) (*dtv1.DataValue, error) {
		value = value*scale + dp.Offset
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("non-finite numeric value")
		}
		return &dtv1.DataValue{Kind: &dtv1.DataValue_DoubleValue{DoubleValue: value}}, nil
	}
	switch kind {
	case "bool":
		if text != "true" && text != "false" && text != "0" && text != "1" {
			return nil, fmt.Errorf("invalid boolean %q", text)
		}
		b, e := strconv.ParseBool(text)
		return &dtv1.DataValue{Kind: &dtv1.DataValue_BoolValue{BoolValue: b}}, e
	case "string":
		return &dtv1.DataValue{Kind: &dtv1.DataValue_StringValue{StringValue: text}}, nil
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		bits := 64
		prefix := "int"
		if strings.HasPrefix(kind, "uint") {
			prefix = "uint"
		}
		if suffix := strings.TrimPrefix(kind, prefix); suffix != "" {
			bits, _ = strconv.Atoi(suffix)
		}
		if prefix == "uint" {
			n, e := strconv.ParseUint(text, 10, bits)
			if e != nil {
				return nil, e
			}
			if scale != 1 || dp.Offset != 0 {
				return scaled(float64(n))
			}
			if n <= math.MaxInt64 {
				return &dtv1.DataValue{Kind: &dtv1.DataValue_IntValue{IntValue: int64(n)}}, nil
			}
			return &dtv1.DataValue{Kind: &dtv1.DataValue_UintValue{UintValue: n}}, nil
		}
		n, e := strconv.ParseInt(text, 10, bits)
		if e != nil {
			return nil, e
		}
		if scale != 1 || dp.Offset != 0 {
			return scaled(float64(n))
		}
		return &dtv1.DataValue{Kind: &dtv1.DataValue_IntValue{IntValue: n}}, nil
	case "float", "float32", "float64", "double":
		n, e := strconv.ParseFloat(text, 64)
		if e != nil {
			return nil, e
		}
		return scaled(n)
	default:
		return nil, fmt.Errorf("unsupported data type %q", kind)
	}
}

func Scalar(text, kind string) (any, error) {
	v, e := Value(text, config.DatapointConfig{DataType: kind})
	if e != nil {
		return nil, e
	}
	switch value := v.Kind.(type) {
	case *dtv1.DataValue_BoolValue:
		return value.BoolValue, nil
	case *dtv1.DataValue_StringValue:
		return value.StringValue, nil
	case *dtv1.DataValue_DoubleValue:
		if kind == "float32" {
			return float32(value.DoubleValue), nil
		}
		return value.DoubleValue, nil
	case *dtv1.DataValue_UintValue:
		return value.UintValue, nil
	case *dtv1.DataValue_IntValue:
		switch kind {
		case "int8":
			return int8(value.IntValue), nil
		case "int16":
			return int16(value.IntValue), nil
		case "int32":
			return int32(value.IntValue), nil
		case "uint8":
			return uint8(value.IntValue), nil
		case "uint16":
			return uint16(value.IntValue), nil
		case "uint32":
			return uint32(value.IntValue), nil
		case "uint64":
			return uint64(value.IntValue), nil
		}
		return value.IntValue, nil
	}
	return nil, fmt.Errorf("unsupported scalar type")
}

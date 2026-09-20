package modbus

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/conversion"
	dterrors "competition2026/product/datatransfer/internal/errors"
)

const (
	RegisterTypeCoil            = "coil"
	RegisterTypeDiscreteInput   = "discrete_input"
	RegisterTypeHoldingRegister = "holding_register"
	RegisterTypeInputRegister   = "input_register"

	DataTypeBool    = "bool"
	DataTypeInt16   = "int16"
	DataTypeUint16  = "uint16"
	DataTypeInt32   = "int32"
	DataTypeUint32  = "uint32"
	DataTypeFloat32 = "float32"
)

type Converter struct {
	connector config.ConnectorConfig
}

type Reading struct {
	Datapoint config.DatapointConfig
	Raw       any
	Timestamp int64
	Error     error
}

func NewConverter(connector config.ConnectorConfig) *Converter {
	return &Converter{connector: connector}
}

// BuildTelemetry 将一轮采集的原始读取转换为单条 TELEMETRY 消息。
// 读取或解码失败的数据点保留为 BAD，记录原始采集时间与失败原因。
func (c *Converter) BuildTelemetry(device config.DeviceConfig, readings []Reading) (*dtv1.DeviceMessage, []error, error) {
	if len(readings) == 0 {
		return nil, nil, nil
	}
	datapoints := make([]*dtv1.Datapoint, 0, len(readings))
	var skipped []error
	for _, reading := range readings {
		value, err := decodeValue(reading.Raw, reading.Datapoint)
		if reading.Error != nil {
			err = reading.Error
		}
		if err != nil {
			skipped = append(skipped, fmt.Errorf("datapoint %q: %w", reading.Datapoint.Key, err))
			datapoints = append(datapoints, &dtv1.Datapoint{Key: reading.Datapoint.Key, Timestamp: reading.Timestamp, Quality: dtv1.DataQuality_BAD, QualityReason: err.Error(), Unit: reading.Datapoint.Unit, TimeSource: "collector"})
			continue
		}
		datapoints = append(datapoints, &dtv1.Datapoint{
			Key:        reading.Datapoint.Key,
			Value:      value,
			Timestamp:  reading.Timestamp,
			Quality:    qualityFromString(reading.Datapoint.Quality),
			Unit:       reading.Datapoint.Unit,
			TimeSource: "collector",
		})
	}
	if len(datapoints) == 0 {
		return nil, skipped, fmt.Errorf("%s: all datapoints failed to decode for device %s", dterrors.CodeConverterFailed, device.DeviceID)
	}
	now := time.Now().UnixMilli()
	return &dtv1.DeviceMessage{
		MessageId: fmt.Sprintf("modbus-%s-%d", device.DeviceID, time.Now().UnixNano()),
		Timestamp: now,
		Direction: dtv1.Direction_UPSTREAM,
		Device: &dtv1.DeviceIdentity{
			DeviceId:    device.DeviceID,
			DeviceName:  device.DeviceName,
			DeviceType:  device.DeviceType,
			ConnectorId: c.connector.ConnectorID,
			Protocol:    c.connector.Protocol,
			Tags:        mergeTags(c.connector.DefaultTags, device.Tags),
		},
		Type: dtv1.MessageType_TELEMETRY,
		Payload: &dtv1.DeviceMessage_Telemetry{
			Telemetry: &dtv1.TelemetryPayload{Datapoints: datapoints},
		},
		Metadata: map[string]string{
			"time_source": "collector",
			"protocol":    c.connector.Protocol,
		},
	}, skipped, nil
}

func decodeValue(raw any, datapoint config.DatapointConfig) (*dtv1.DataValue, error) {
	switch value := raw.(type) {
	case bool:
		return &dtv1.DataValue{Kind: &dtv1.DataValue_BoolValue{BoolValue: value}}, nil
	case []uint16:
		return decodeRegisters(value, datapoint)
	default:
		return nil, fmt.Errorf("%s: unsupported raw value type %T", dterrors.CodeConverterFailed, raw)
	}
}

func orderedRegisters(registers []uint16, byteOrder, wordOrder string) ([]uint16, error) {
	if byteOrder != "" && byteOrder != "big" && byteOrder != "little" {
		return nil, fmt.Errorf("invalid byte_order %q", byteOrder)
	}
	if wordOrder != "" && wordOrder != "big" && wordOrder != "little" {
		return nil, fmt.Errorf("invalid word_order %q", wordOrder)
	}
	out := append([]uint16(nil), registers...)
	if byteOrder == "little" {
		for i, v := range out {
			out[i] = v>>8 | v<<8
		}
	}
	if wordOrder == "little" {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}
func decodeRegisters(registers []uint16, datapoint config.DatapointConfig) (*dtv1.DataValue, error) {
	width := int(registersQuantity(datapoint))
	if width > 4 || width < 1 || len(registers) < width {
		return nil, fmt.Errorf("invalid register width for %s", datapoint.DataType)
	}
	words, err := orderedRegisters(registers[:width], datapoint.ByteOrder, datapoint.WordOrder)
	if err != nil {
		return nil, err
	}
	bits := uint64(0)
	for _, word := range words {
		bits = bits<<16 | uint64(word)
	}
	if datapoint.BitLength > 0 {
		if int(datapoint.BitOffset)+int(datapoint.BitLength) > width*16 {
			return nil, fmt.Errorf("bit field exceeds register width")
		}
		bits >>= datapoint.BitOffset
		if datapoint.BitLength < 64 {
			bits &= (uint64(1) << datapoint.BitLength) - 1
		}
	}
	var raw any
	switch strings.ToLower(datapoint.DataType) {
	case "bool":
		raw = bits != 0
	case "int16":
		raw = int16(bits)
	case "", "uint16":
		raw = uint16(bits)
	case "int32":
		raw = int32(bits)
	case "uint32":
		raw = uint32(bits)
	case "int64":
		raw = int64(bits)
	case "uint64":
		raw = bits
	case "float32":
		raw = math.Float32frombits(uint32(bits))
	case "float64", "double":
		raw = math.Float64frombits(bits)
	default:
		return nil, fmt.Errorf("unsupported data_type %q", datapoint.DataType)
	}
	return conversion.Value(raw, datapoint)
}
func encodeMapped(raw string, mapping config.ActionMapping) ([]uint16, error) {
	scale := 1.0
	if mapping.Scale != nil {
		scale = *mapping.Scale
	}
	if scale == 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		return nil, fmt.Errorf("invalid reverse scale")
	}
	if scale != 1 || mapping.Offset != 0 {
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, err
		}
		n = (n - mapping.Offset) / scale
		if strings.HasPrefix(mapping.DataType, "int") || strings.HasPrefix(mapping.DataType, "uint") || mapping.DataType == "" {
			if math.Abs(n-math.Round(n)) > 1e-8 {
				return nil, fmt.Errorf("reverse conversion is not integral")
			}
			n = math.Round(n)
		}
		raw = strconv.FormatFloat(n, 'f', -1, 64)
	}
	words, err := encodeRegisterValues(raw, mapping.DataType)
	if err != nil {
		return nil, err
	}
	return orderedRegisters(words, mapping.ByteOrder, mapping.WordOrder)
}

func encodeRegisterValues(raw string, dataType string) ([]uint16, error) {
	switch strings.ToLower(dataType) {
	case "int64", "uint64", "float64", "double":
		var bits uint64
		var err error
		switch strings.ToLower(dataType) {
		case "int64":
			var n int64
			n, err = strconv.ParseInt(raw, 10, 64)
			bits = uint64(n)
		case "uint64":
			bits, err = strconv.ParseUint(raw, 10, 64)
		default:
			var n float64
			n, err = strconv.ParseFloat(raw, 64)
			if math.IsNaN(n) || math.IsInf(n, 0) {
				err = fmt.Errorf("non-finite value")
			}
			bits = math.Float64bits(n)
		}
		if err != nil {
			return nil, err
		}
		return []uint16{uint16(bits >> 48), uint16(bits >> 32), uint16(bits >> 16), uint16(bits)}, nil
	case "", DataTypeUint16:
		value, err := strconv.ParseUint(raw, 10, 16)
		if err != nil {
			return nil, err
		}
		return []uint16{uint16(value)}, nil
	case DataTypeInt16:
		value, err := strconv.ParseInt(raw, 10, 16)
		if err != nil {
			return nil, err
		}
		return []uint16{uint16(int16(value))}, nil
	case DataTypeInt32:
		value, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return nil, err
		}
		return uint32ToRegisters(uint32(int32(value))), nil
	case DataTypeUint32:
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return nil, err
		}
		return uint32ToRegisters(uint32(value)), nil
	case DataTypeFloat32:
		value, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return nil, err
		}
		return uint32ToRegisters(math.Float32bits(float32(value))), nil
	default:
		return nil, fmt.Errorf("%s: unsupported register data_type %q", dterrors.CodeConverterInvalid, dataType)
	}
}

func uint32ToRegisters(value uint32) []uint16 {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, value)
	return []uint16{
		binary.BigEndian.Uint16(buf[0:2]),
		binary.BigEndian.Uint16(buf[2:4]),
	}
}

func dataValueToString(value *dtv1.DataValue) (string, error) {
	switch typed := value.GetKind().(type) {
	case *dtv1.DataValue_BoolValue:
		return strconv.FormatBool(typed.BoolValue), nil
	case *dtv1.DataValue_DoubleValue:
		return strconv.FormatFloat(typed.DoubleValue, 'f', -1, 64), nil
	case *dtv1.DataValue_UintValue:
		return strconv.FormatUint(typed.UintValue, 10), nil
	case *dtv1.DataValue_IntValue:
		return strconv.FormatInt(typed.IntValue, 10), nil
	case *dtv1.DataValue_StringValue:
		return typed.StringValue, nil
	default:
		return "", fmt.Errorf("%s: unsupported command value", dterrors.CodeConverterInvalid)
	}
}

func qualityFromString(value string) dtv1.DataQuality {
	switch strings.ToLower(value) {
	case "bad":
		return dtv1.DataQuality_BAD
	case "uncertain":
		return dtv1.DataQuality_UNCERTAIN
	default:
		return dtv1.DataQuality_GOOD
	}
}

func mergeTags(base, override map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(override))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		out[key] = value
	}
	return out
}

func registersQuantity(datapoint config.DatapointConfig) uint16 {
	if datapoint.Quantity > 0 {
		return datapoint.Quantity
	}
	switch strings.ToLower(datapoint.DataType) {
	case "int64", "uint64", "float64", "double":
		return 4
	case DataTypeInt32, DataTypeUint32, DataTypeFloat32:
		return 2
	default:
		return 1
	}
}

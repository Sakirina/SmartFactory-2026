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
}

func NewConverter(connector config.ConnectorConfig) *Converter {
	return &Converter{connector: connector}
}

func (c *Converter) BuildTelemetry(device config.DeviceConfig, readings []Reading) (*dtv1.DeviceMessage, error) {
	if len(readings) == 0 {
		return nil, nil
	}
	datapoints := make([]*dtv1.Datapoint, 0, len(readings))
	for _, reading := range readings {
		value, err := decodeValue(reading.Raw, reading.Datapoint)
		if err != nil {
			return nil, err
		}
		datapoints = append(datapoints, &dtv1.Datapoint{
			Key:       reading.Datapoint.Key,
			Value:     value,
			Timestamp: reading.Timestamp,
			Quality:   qualityFromString(reading.Datapoint.Quality),
			Unit:      reading.Datapoint.Unit,
		})
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
	}, nil
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

func decodeRegisters(registers []uint16, datapoint config.DatapointConfig) (*dtv1.DataValue, error) {
	dataType := strings.ToLower(datapoint.DataType)
	if dataType == "" {
		dataType = DataTypeUint16
	}
	switch dataType {
	case DataTypeBool:
		if len(registers) < 1 {
			return nil, fmt.Errorf("%s: %s requires one register", dterrors.CodeConverterFailed, dataType)
		}
		return &dtv1.DataValue{Kind: &dtv1.DataValue_BoolValue{BoolValue: registers[0] != 0}}, nil
	case DataTypeInt16:
		if len(registers) < 1 {
			return nil, fmt.Errorf("%s: %s requires one register", dterrors.CodeConverterFailed, dataType)
		}
		return numericValue(float64(int16(registers[0])), datapoint, true), nil
	case DataTypeUint16:
		if len(registers) < 1 {
			return nil, fmt.Errorf("%s: %s requires one register", dterrors.CodeConverterFailed, dataType)
		}
		return numericValue(float64(registers[0]), datapoint, true), nil
	case DataTypeInt32:
		if len(registers) < 2 {
			return nil, fmt.Errorf("%s: %s requires two registers", dterrors.CodeConverterFailed, dataType)
		}
		value := int32(uint32(registers[0])<<16 | uint32(registers[1]))
		return numericValue(float64(value), datapoint, true), nil
	case DataTypeUint32:
		if len(registers) < 2 {
			return nil, fmt.Errorf("%s: %s requires two registers", dterrors.CodeConverterFailed, dataType)
		}
		value := uint32(registers[0])<<16 | uint32(registers[1])
		return numericValue(float64(value), datapoint, true), nil
	case DataTypeFloat32:
		if len(registers) < 2 {
			return nil, fmt.Errorf("%s: %s requires two registers", dterrors.CodeConverterFailed, dataType)
		}
		bits := uint32(registers[0])<<16 | uint32(registers[1])
		return numericValue(float64(math.Float32frombits(bits)), datapoint, false), nil
	default:
		return nil, fmt.Errorf("%s: unsupported data_type %q", dterrors.CodeConverterInvalid, datapoint.DataType)
	}
}

func numericValue(raw float64, datapoint config.DatapointConfig, integral bool) *dtv1.DataValue {
	scale := 1.0
	if datapoint.Scale != nil {
		scale = *datapoint.Scale
	}
	value := raw*scale + datapoint.Offset
	if integral && scale == 1 && datapoint.Offset == 0 {
		return &dtv1.DataValue{Kind: &dtv1.DataValue_IntValue{IntValue: int64(value)}}
	}
	return &dtv1.DataValue{Kind: &dtv1.DataValue_DoubleValue{DoubleValue: value}}
}

func encodeRegisterValues(raw string, dataType string) ([]uint16, error) {
	switch strings.ToLower(dataType) {
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
	case DataTypeInt32, DataTypeUint32, DataTypeFloat32:
		return 2
	default:
		return 1
	}
}

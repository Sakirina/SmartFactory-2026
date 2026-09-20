package simulator

import (
	"errors"
	"fmt"
	modbus "github.com/simonvetter/modbus"
)

type modbusState struct{ state *State }

func (h *modbusState) device(unit uint8) (string, error) {
	switch unit {
	case 1:
		return "climate-1", nil
	case 2:
		return "agv-1", nil
	}
	return "", modbus.ErrIllegalDataAddress
}
func (h *modbusState) HandleCoils(r *modbus.CoilsRequest) ([]bool, error) {
	device, e := h.device(r.UnitId)
	if e != nil {
		return nil, e
	}
	key := "fan"
	if device == "agv-1" {
		key = "stopped"
	}
	out := make([]bool, r.Quantity)
	for i := range out {
		addr := int(r.Addr) + i
		k := key
		if addr == 1 {
			k = "interlock"
		} else if device == "climate-1" && addr >= 2 && addr <= 4 {
			k = []string{"heater", "humidifier", "dehumidifier"}[addr-2]
		} else if addr != 0 {
			return nil, modbus.ErrIllegalDataAddress
		}
		if r.IsWrite {
			if k == "interlock" {
				return nil, errors.New("interlock is read only over control protocol")
			}
			if h.state.Snapshot()[device]["interlock"] == true {
				return nil, modbus.ErrServerDeviceFailure
			}
			if e = h.state.Set(device, k, r.Args[i]); e != nil {
				return nil, e
			}
		}
		out[i] = h.state.Snapshot()[device][k].(bool)
	}
	return out, nil
}
func (h *modbusState) HandleDiscreteInputs(r *modbus.DiscreteInputsRequest) ([]bool, error) {
	return h.HandleCoils(&modbus.CoilsRequest{UnitId: r.UnitId, Addr: r.Addr, Quantity: r.Quantity})
}
func (h *modbusState) HandleHoldingRegisters(r *modbus.HoldingRegistersRequest) ([]uint16, error) {
	device, e := h.device(r.UnitId)
	if e != nil {
		return nil, e
	}
	out := make([]uint16, r.Quantity)
	for i := range out {
		addr := int(r.Addr) + i
		key := "temperature"
		scale := 10.0
		if device == "agv-1" {
			key = "distance"
			scale = 1
		} else if addr == 1 {
			key = "humidity"
		} else if addr != 0 {
			return nil, modbus.ErrIllegalDataAddress
		}
		if r.IsWrite {
			if e = h.state.Set(device, key, float64(r.Args[i])/scale); e != nil {
				return nil, e
			}
		}
		out[i] = uint16(h.state.Snapshot()[device][key].(float64) * scale)
	}
	return out, nil
}
func (h *modbusState) HandleInputRegisters(r *modbus.InputRegistersRequest) ([]uint16, error) {
	return h.HandleHoldingRegisters(&modbus.HoldingRegistersRequest{UnitId: r.UnitId, Addr: r.Addr, Quantity: r.Quantity})
}
func modbusURL(host string) string { return fmt.Sprintf("tcp://%s", host) }

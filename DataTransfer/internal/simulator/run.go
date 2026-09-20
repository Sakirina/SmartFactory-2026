package simulator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"competition2026/product/datatransfer/internal/config"
	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	"github.com/gopcua/opcua/uasc"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	modbus "github.com/simonvetter/modbus"
	"gopkg.in/yaml.v3"
)

type Options struct {
	StatePath, ConfigPath, Modbus, MQTT, OPCUA, HTTP string
	Interval                                         time.Duration
	FleetCount                                       int
	FleetPrefix, GRPC, Management                    string
	CommandReplyDelay                                time.Duration
}
type Simulator struct {
	State       *State
	Options     Options
	Messages    atomic.Int64
	NamespaceID uint16
	publisher   paho.Client
	notify      func(string)
}

func (s *Simulator) publish(topic string, payload any) error {
	raw, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	token := s.publisher.Publish(topic, 1, false, raw)
	if !token.WaitTimeout(5 * time.Second) {
		return errors.New("simulator MQTT publish timed out")
	}
	if e = token.Error(); e == nil {
		s.Messages.Add(1)
	}
	return e
}
func (s *Simulator) Pulse(count, duplicateEvery int) error {
	if count < 0 || count > 100000 {
		return errors.New("count must be between 0 and 100000")
	}
	for i := 0; i < count; i++ {
		payload, e := s.State.NextPulse()
		if e != nil {
			return e
		}
		if payload == nil {
			continue
		}
		if e = s.publish("devices/counter-1/event", payload); e != nil {
			return e
		}
		if duplicateEvery > 0 && i%duplicateEvery == 0 {
			if e = s.publish("devices/counter-1/event", payload); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Simulator) ReplayPending() error {
	events, e := s.State.Pending(1000)
	if e != nil {
		return e
	}
	for _, event := range events {
		if e = s.publish(event.Topic, event.Payload); e != nil {
			return e
		}
	}
	return nil
}
func (s *Simulator) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /commands", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.State.DB.QueryContext(r.Context(), "SELECT id,body,result,received_ns FROM commands ORDER BY received_ns DESC LIMIT 10000")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		commands := []map[string]any{}
		for rows.Next() {
			var id, body, result string
			var at int64
			if err = rows.Scan(&id, &body, &result, &at); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			commands = append(commands, map[string]any{"command_id": id, "body": json.RawMessage(body), "result": json.RawMessage(result), "received_ns": at})
		}
		_ = json.NewEncoder(w).Encode(commands)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		var pending int64
		_ = s.State.DB.QueryRow("SELECT COUNT(*) FROM pending_events").Scan(&pending)
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": s.State.Snapshot(), "mqtt_messages": s.Messages.Load(), "opcua_namespace": s.NamespaceID, "pending_events": pending})
	})
	mux.HandleFunc("POST /state", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DeviceID string         `json:"device_id"`
			Values   map[string]any `json:"values"`
		}
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		for key, value := range req.Values {
			if e := s.State.Set(req.DeviceID, key, value); e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
			if s.notify != nil {
				s.notify(req.DeviceID + "." + key)
			}
		}
		_ = json.NewEncoder(w).Encode(s.State.Snapshot())
	})
	mux.HandleFunc("POST /pulses", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Count          int `json:"count"`
			DuplicateEvery int `json:"duplicate_every"`
		}
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if e := s.Pulse(req.Count, req.DuplicateEvery); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		_ = json.NewEncoder(w).Encode(s.State.Snapshot())
	})
	mux.HandleFunc("POST /command/{device}", func(w http.ResponseWriter, r *http.Request) {
		var command Command
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&command); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		result, e := s.State.Execute(r.PathValue("device"), command)
		if e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	})
	return mux
}
func Run(ctx context.Context, o Options) error {
	if o.FleetCount > 0 {
		return RunFleet(ctx, o)
	}
	state, e := OpenState(o.StatePath)
	if e != nil {
		return e
	}
	defer state.DB.Close()
	sim := &Simulator{State: state, Options: o}
	mb, e := modbus.NewServer(&modbus.ServerConfiguration{URL: modbusURL(o.Modbus), MaxClients: 1024, Logger: log.New(io.Discard, "", 0)}, &modbusState{state})
	if e != nil {
		return e
	}
	if e = mb.Start(); e != nil {
		return e
	}
	defer mb.Stop()
	broker := mqtt.New(&mqtt.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if e = broker.AddHook(new(auth.AllowHook), nil); e != nil {
		return e
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "simulation", Address: o.MQTT})
	if e = broker.AddListener(tcp); e != nil {
		return e
	}
	if e = broker.Serve(); e != nil {
		return e
	}
	defer broker.Close()
	sim.publisher = paho.NewClient(paho.NewClientOptions().AddBroker("tcp://" + tcp.Address()).SetClientID("smartfactory-simulator").SetOrderMatters(false).SetAutoReconnect(true))
	token := sim.publisher.Connect()
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		return fmt.Errorf("simulator broker client: %v", token.Error())
	}
	defer sim.publisher.Disconnect(100)
	token = sim.publisher.Subscribe("devices/+/ack", 1, func(_ paho.Client, message paho.Message) {
		parts := strings.Split(message.Topic(), "/")
		var ack struct {
			MessageID string `json:"message_id"`
			Committed bool   `json:"committed"`
		}
		if len(parts) == 3 && json.Unmarshal(message.Payload(), &ack) == nil && ack.Committed && ack.MessageID != "" {
			if e := state.Acknowledge(parts[1], ack.MessageID); e != nil {
				slog.Warn("simulation acknowledgement failed", "error", e)
			}
		}
	})
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		return fmt.Errorf("simulator acknowledgement subscription: %v", token.Error())
	}
	token = sim.publisher.Subscribe("devices/+/command", 1, func(_ paho.Client, message paho.Message) {
		pieces := strings.Split(message.Topic(), "/")
		if len(pieces) != 3 {
			return
		}
		var command Command
		if json.Unmarshal(message.Payload(), &command) != nil {
			return
		}
		result, e := state.ExecuteWithReply(pieces[1], command)
		if e != nil {
			result = map[string]any{"command_id": command.ID, "message_id": "rejected:" + command.ID, "status": "REJECTED", "message": e.Error()}
		}
		_ = sim.publish("devices/"+pieces[1]+"/cmd-response", result)
	})
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		return fmt.Errorf("simulator command subscription: %v", token.Error())
	}
	host, portText, e := net.SplitHostPort(o.OPCUA)
	if e != nil {
		return e
	}
	port, e := strconv.Atoi(portText)
	if e != nil {
		return e
	}
	opc := server.New(server.EndPoint(host, port), server.EnableSecurity("None", ua.MessageSecurityModeNone), server.EnableAuthMode(ua.UserTokenTypeAnonymous), server.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	base := server.NewMapNamespace(opc, "urn:smartfactory:simulation")
	for device, fields := range state.Snapshot() {
		for key, value := range fields {
			base.Data[device+"."+key] = value
		}
	}
	ns := &namespace{MapNamespace: base, state: state}
	opc.AddNamespace(ns)
	sim.NamespaceID = ns.ID()
	sim.notify = base.ChangeNotification
	opc.RegisterHandler(id.CallRequest_Encoding_DefaultBinary, func(_ *uasc.SecureChannel, r ua.Request, _ uint32) (ua.Response, error) {
		req, ok := r.(*ua.CallRequest)
		if !ok {
			return nil, ua.StatusBadInvalidArgument
		}
		response := &ua.CallResponse{ResponseHeader: &ua.ResponseHeader{Timestamp: time.Now(), RequestHandle: req.RequestHeader.RequestHandle, ServiceResult: ua.StatusOK}, Results: []*ua.CallMethodResult{}}
		for _, call := range req.MethodsToCall {
			result := &ua.CallMethodResult{StatusCode: ua.StatusBadMethodInvalid}
			if call.MethodID.StringID() == "gas-1.set_extractor" && len(call.InputArguments) == 1 {
				value, ok := call.InputArguments[0].Value().(bool)
				if !ok {
					result.StatusCode = ua.StatusBadTypeMismatch
				} else if state.Snapshot()["gas-1"]["interlock"] == true {
					result.StatusCode = ua.StatusBadUserAccessDenied
				} else if state.Set("gas-1", "extractor", value) == nil {
					result.StatusCode = ua.StatusOK
					result.OutputArguments = []*ua.Variant{ua.MustVariant(value)}
					sim.notify("gas-1.extractor")
				}
			}
			response.Results = append(response.Results, result)
		}
		return response, nil
	})
	if e = opc.Start(ctx); e != nil {
		return e
	}
	defer opc.Close()
	if o.ConfigPath != "" {
		if e = os.MkdirAll(filepath.Dir(o.ConfigPath), 0700); e != nil {
			return e
		}
		cfg := sim.Config()
		raw, e := yaml.Marshal(cfg)
		if e != nil {
			return e
		}
		if e = os.WriteFile(o.ConfigPath, raw, 0600); e != nil {
			return e
		}
	}
	listener, e := net.Listen("tcp", o.HTTP)
	if e != nil {
		return e
	}
	web := &http.Server{Handler: sim.Handler(), ReadHeaderTimeout: 5 * time.Second}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = web.Serve(listener) }()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = web.Shutdown(closeCtx)
		wg.Wait()
	}()
	ticker := time.NewTicker(o.Interval)
	defer ticker.Stop()
	replay := time.NewTicker(time.Second)
	defer replay.Stop()
	slog.Info("protocol simulator ready", "modbus", o.Modbus, "mqtt", o.MQTT, "opcua", o.OPCUA, "http", o.HTTP, "namespace", sim.NamespaceID)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-replay.C:
			if e := sim.ReplayPending(); e != nil {
				slog.Warn("simulation event replay deferred", "error", e)
			}
		case <-ticker.C:
			state.Tick()
			for _, device := range []string{"light-1", "counter-1"} {
				values := state.Snapshot()[device]
				values["message_id"] = fmt.Sprintf("%s:%d", device, time.Now().UnixNano())
				values["timestamp"] = time.Now().UnixMilli()
				if e = sim.publish("devices/"+device+"/telemetry", values); e != nil {
					slog.Warn("simulation publish deferred", "device", device, "error", e)
				}
			}
			for key := range state.Snapshot()["gas-1"] {
				sim.notify("gas-1." + key)
			}
		}
	}
}
func (s *Simulator) Config() config.Config {
	cfg := config.Defaults()
	cfg.Environment = config.EnvDevelopment
	cfg.Management.Addr = "127.0.0.1:18082"
	cfg.GRPC.Addr = "127.0.0.1:50051"
	cfg.Runtime.StatePath = filepath.Join(filepath.Dir(s.Options.StatePath), "datatransfer-state.db")
	if s.Options.StatePath == ":memory:" {
		cfg.Runtime.StatePath = ":memory:"
	}
	scale := 0.1
	climate := config.DeviceConfig{DeviceID: "climate-1", DeviceName: "温湿度与通风设备", UnitID: 1, Protocol: "modbus_tcp", Datapoints: []config.DatapointConfig{{Key: "temperature", RegisterType: "holding_register", Address: 0, DataType: "uint16", Scale: &scale, Unit: "°C"}, {Key: "humidity", RegisterType: "holding_register", Address: 1, DataType: "uint16", Scale: &scale, Unit: "%"}, {Key: "fan", RegisterType: "coil", Address: 0, DataType: "bool"}, {Key: "interlock", RegisterType: "coil", Address: 1, DataType: "bool"}}, ActionMappings: map[string]config.ActionMapping{"set_fan": {Type: "write_single_coil", Address: 0, Param: "value"}}}
	for i, key := range []string{"heater", "humidifier", "dehumidifier"} {
		climate.Datapoints = append(climate.Datapoints, config.DatapointConfig{Key: key, RegisterType: "coil", Address: uint16(i + 2), DataType: "bool"})
		climate.ActionMappings["set_"+key] = config.ActionMapping{Type: "write_single_coil", Address: uint16(i + 2), Param: "value"}
	}
	agv := config.DeviceConfig{DeviceID: "agv-1", DeviceName: "AGV避障", UnitID: 2, Protocol: "modbus_tcp", Datapoints: []config.DatapointConfig{{Key: "distance", RegisterType: "holding_register", DataType: "uint16", Unit: "cm"}, {Key: "stopped", RegisterType: "coil", DataType: "bool"}, {Key: "interlock", RegisterType: "coil", Address: 1, DataType: "bool"}}, ActionMappings: map[string]config.ActionMapping{"stop": {Type: "write_single_coil", Address: 0, Value: "true"}, "resume": {Type: "write_single_coil", Address: 0, Value: "false"}}}
	cfg.Connectors = append(cfg.Connectors, config.ConnectorConfig{ConnectorID: "simulation-modbus", Protocol: "modbus_tcp", Connection: config.ConnectionConfig{URL: modbusURL(s.Options.Modbus), TimeoutMillis: 500}, Polling: config.PollingConfig{IntervalMillis: 500}, Devices: []config.DeviceConfig{climate, agv}})
	light := config.DeviceConfig{DeviceID: "light-1", DeviceName: "红外照明", Datapoints: []config.DatapointConfig{{Key: "presence", Source: "presence", DataType: "bool"}, {Key: "light", Source: "light", DataType: "bool"}, {Key: "interlock", Source: "interlock", DataType: "bool"}}}
	counter := config.DeviceConfig{DeviceID: "counter-1", DeviceName: "货物计数", Datapoints: []config.DatapointConfig{{Key: "total", Source: "total", DataType: "uint64", Unit: "件"}, {Key: "enabled", Source: "enabled", DataType: "bool"}, {Key: "interlock", Source: "interlock", DataType: "bool"}}}
	cfg.Connectors = append(cfg.Connectors, config.ConnectorConfig{ConnectorID: "simulation-mqtt", Protocol: "mqtt_device", Connection: config.ConnectionConfig{URL: "tcp://" + s.Options.MQTT, MQTTVersion: "5.0", TimeoutMillis: 2000}, Devices: []config.DeviceConfig{light, counter}})
	gas := config.DeviceConfig{DeviceID: "gas-1", DeviceName: "危气监测", ActionMappings: map[string]config.ActionMapping{"set_extractor": {Type: "write", NodeID: fmt.Sprintf("ns=%d;s=gas-1.extractor", s.NamespaceID), Param: "value", DataType: "bool"}}}
	for _, key := range []string{"smoke", "combustible", "co", "extractor", "interlock"} {
		kind := "float64"
		if key == "extractor" || key == "interlock" {
			kind = "bool"
		}
		gas.Datapoints = append(gas.Datapoints, config.DatapointConfig{Key: key, NodeID: fmt.Sprintf("ns=%d;s=gas-1.%s", s.NamespaceID, key), DataType: kind})
	}
	cfg.Connectors = append(cfg.Connectors, config.ConnectorConfig{ConnectorID: "simulation-opcua", Protocol: "opcua", Connection: config.ConnectionConfig{URL: "opc.tcp://" + s.Options.OPCUA, TimeoutMillis: 2000}, Polling: config.PollingConfig{IntervalMillis: 500}, Devices: []config.DeviceConfig{gas}})
	return cfg
}

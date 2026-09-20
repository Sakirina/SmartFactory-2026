package simulator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"competition2026/product/datatransfer/internal/config"
	paho "github.com/eclipse/paho.mqtt.golang"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"gopkg.in/yaml.v3"
)

// RunFleet gives every simulated device independent state and a real MQTT topic.
func RunFleet(ctx context.Context, o Options) error {
	if o.FleetCount < 1 || o.FleetCount > 500 || o.FleetPrefix == "" || strings.ContainsAny(o.FleetPrefix, "/+# ") {
		return fmt.Errorf("invalid fleet count or prefix")
	}
	initial := map[string]map[string]any{}
	devices := []config.DeviceConfig{}
	for i := 0; i < o.FleetCount; i++ {
		id := fmt.Sprintf("%s-device-%03d", o.FleetPrefix, i)
		initial[id] = map[string]any{"smoke": 0.0, "extractor": false, "interlock": false}
		devices = append(devices, config.DeviceConfig{DeviceID: id, DeviceName: id, Datapoints: []config.DatapointConfig{{Key: "smoke", Source: "smoke", DataType: "float64"}, {Key: "extractor", Source: "extractor", DataType: "bool"}, {Key: "interlock", Source: "interlock", DataType: "bool"}}})
	}
	state, err := openState(o.StatePath, initial)
	if err != nil {
		return err
	}
	defer state.DB.Close()
	if len(state.Devices) != len(initial) {
		return fmt.Errorf("existing simulation state belongs to another profile")
	}
	for id := range initial {
		if state.Devices[id] == nil {
			return fmt.Errorf("existing simulation state has different device identifiers")
		}
	}
	sim := &Simulator{State: state, Options: o}
	broker := mqtt.New(&mqtt.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err = broker.AddHook(new(auth.AllowHook), nil); err != nil {
		return err
	}
	listener := listeners.NewTCP(listeners.Config{ID: o.FleetPrefix, Address: o.MQTT})
	if err = broker.AddListener(listener); err != nil {
		return err
	}
	if err = broker.Serve(); err != nil {
		return err
	}
	defer broker.Close()
	sim.publisher = paho.NewClient(paho.NewClientOptions().AddBroker("tcp://" + listener.Address()).SetClientID("simulation-" + o.FleetPrefix).SetOrderMatters(false).SetAutoReconnect(true))
	token := sim.publisher.Connect()
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		return fmt.Errorf("fleet broker client: %v", token.Error())
	}
	defer sim.publisher.Disconnect(100)
	token = sim.publisher.Subscribe("devices/+/command", 1, func(_ paho.Client, message paho.Message) {
		parts := strings.Split(message.Topic(), "/")
		var command Command
		if len(parts) != 3 || json.Unmarshal(message.Payload(), &command) != nil {
			return
		}
		result, e := state.ExecuteWithReply(parts[1], command)
		if e != nil {
			result = map[string]any{"command_id": command.ID, "message_id": "rejected:" + command.ID, "status": "REJECTED", "message": e.Error()}
		}
		if o.CommandReplyDelay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(o.CommandReplyDelay):
			}
		}
		_ = sim.publish("devices/"+parts[1]+"/cmd-response", result)
	})
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		return fmt.Errorf("fleet command subscription: %v", token.Error())
	}
	token = sim.publisher.Subscribe("devices/+/ack", 1, func(_ paho.Client, message paho.Message) {
		parts := strings.Split(message.Topic(), "/")
		var ack struct {
			MessageID string `json:"message_id"`
			Committed bool   `json:"committed"`
		}
		if len(parts) == 3 && json.Unmarshal(message.Payload(), &ack) == nil && ack.Committed {
			_ = state.Acknowledge(parts[1], ack.MessageID)
		}
	})
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		return fmt.Errorf("fleet ack subscription: %v", token.Error())
	}
	cfg := config.Defaults()
	cfg.Environment = config.EnvDevelopment
	cfg.MQTT.Enabled = false
	cfg.Buffer.Enabled = false
	cfg.Runtime.StatePath = ":memory:"
	if o.StatePath != ":memory:" {
		cfg.Runtime.StatePath = filepath.Join(filepath.Dir(o.StatePath), "datatransfer-state.db")
	}
	cfg.GRPC.Addr = o.GRPC
	cfg.Management.Addr = o.Management
	cfg.Connectors = []config.ConnectorConfig{{ConnectorID: o.FleetPrefix + "-mqtt", Protocol: "mqtt_device", Connection: config.ConnectionConfig{URL: "tcp://" + listener.Address(), MQTTVersion: "5.0", TimeoutMillis: 2000}, Devices: devices}}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(o.ConfigPath), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(o.ConfigPath, raw, 0600); err != nil {
		return err
	}
	server := &http.Server{Addr: o.HTTP, Handler: sim.Handler(), ReadHeaderTimeout: 5 * time.Second}
	failed := make(chan error, 1)
	go func() { failed <- server.ListenAndServe() }()
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(stop)
	}()
	ticker := time.NewTicker(o.Interval)
	defer ticker.Stop()
	var sequence uint64
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failed:
			return err
		case <-ticker.C:
			sequence++
			for id, values := range state.Snapshot() {
				values["message_id"] = fmt.Sprintf("%s:%d:%d", id, time.Now().UnixNano(), sequence)
				values["timestamp"] = time.Now().UnixMilli()
				values["source_sequence"] = sequence
				if err = sim.publish("devices/"+id+"/telemetry", values); err != nil {
					return err
				}
			}
			if err = sim.ReplayPending(); err != nil {
				return err
			}
		}
	}
}

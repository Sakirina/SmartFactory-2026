// http-plugin reads and controls the protocol simulator through its scenario API.
package main

import (
	"bytes"
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/pkg/plugin"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type adapter struct {
	config struct {
		ID         string `json:"connector_id"`
		Connection struct {
			URL string `json:"url"`
		} `json:"connection"`
		Devices []struct {
			ID string `json:"device_id"`
		} `json:"devices"`
	}
}

func (a *adapter) Init(raw json.RawMessage) error {
	if e := json.Unmarshal(raw, &a.config); e != nil {
		return e
	}
	if a.config.ID == "" || len(a.config.Devices) == 0 || !strings.HasPrefix(a.config.Connection.URL, "http://127.0.0.1:") {
		return errors.New("example requires a loopback simulator URL and configured devices")
	}
	return nil
}
func (a *adapter) request(ctx context.Context, method, path string, value any, out any) error {
	var body io.Reader
	if value != nil {
		raw, e := json.Marshal(value)
		if e != nil {
			return e
		}
		body = bytes.NewReader(raw)
	}
	request, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.config.Connection.URL, "/")+path, body)
	if e != nil {
		return e
	}
	request.Header.Set("Content-Type", "application/json")
	response, e := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("simulator returned %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.UseNumber()
	return decoder.Decode(out)
}
func (a *adapter) Run(ctx context.Context, emit func(*dt.DeviceMessage) error) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		var snapshot struct {
			Devices map[string]map[string]any `json:"devices"`
		}
		if e := a.request(ctx, "GET", "/state", nil, &snapshot); e != nil {
			return e
		}
		for _, device := range a.config.Devices {
			points := []*dt.Datapoint{}
			for key, value := range snapshot.Devices[device.ID] {
				out := &dt.DataValue{}
				switch v := value.(type) {
				case bool:
					out.Kind = &dt.DataValue_BoolValue{BoolValue: v}
				case json.Number:
					if n, e := v.Int64(); e == nil {
						out.Kind = &dt.DataValue_IntValue{IntValue: n}
					} else {
						f, e := v.Float64()
						if e != nil {
							return e
						}
						out.Kind = &dt.DataValue_DoubleValue{DoubleValue: f}
					}
				default:
					continue
				}
				points = append(points, &dt.Datapoint{Key: key, Value: out, Timestamp: time.Now().UnixMilli(), Quality: dt.DataQuality_GOOD, TimeSource: "collector"})
			}
			if e := emit(&dt.DeviceMessage{MessageId: fmt.Sprintf("plugin:%s:%d", device.ID, time.Now().UnixNano()), Timestamp: time.Now().UnixMilli(), Direction: dt.Direction_UPSTREAM, Device: &dt.DeviceIdentity{DeviceId: device.ID, ConnectorId: a.config.ID, Protocol: "process"}, Type: dt.MessageType_TELEMETRY, Payload: &dt.DeviceMessage_Telemetry{Telemetry: &dt.TelemetryPayload{Datapoints: points}}}); e != nil {
				return e
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (a *adapter) Command(ctx context.Context, message *dt.DeviceMessage) (*dt.CommandResponsePayload, error) {
	if message.Type != dt.MessageType_CONTROL {
		return nil, errors.New("example supports control commands")
	}
	var result struct {
		CommandID string `json:"command_id"`
		Status    string `json:"status"`
		Message   string `json:"message"`
	}
	e := a.request(ctx, "POST", "/command/"+message.GetDevice().GetDeviceId(), map[string]any{"command_id": message.CommandId, "action": message.GetControl().Action, "params": message.GetControl().Params, "start_deadline_ms": message.GetControl().GetOptions().GetStartDeadlineMs()}, &result)
	if e != nil {
		return nil, e
	}
	status := dt.CommandStatus_RESULT_UNKNOWN
	if value, ok := dt.CommandStatus_value[result.Status]; ok {
		status = dt.CommandStatus(value)
	}
	return &dt.CommandResponsePayload{CommandId: result.CommandID, Status: status, Message: result.Message}, nil
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := plugin.Serve(ctx, &adapter{}); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

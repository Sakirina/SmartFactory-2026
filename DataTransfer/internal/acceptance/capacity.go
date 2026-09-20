package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	modbus "github.com/simonvetter/modbus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/yaml.v3"
)

type CapacityOptions struct {
	Binary, Directory        string
	OPCUAPython              string
	StateStorage             string
	Duration, Warmup, Period time.Duration
	Commands                 int
}
type capacityState struct {
	active         atomic.Bool
	modbus         [201]atomic.Uint32
	opcua          [100]atomic.Uint32
	mqttProduced   atomic.Int64
	modbusProduced atomic.Int64
	opcuaProduced  atomic.Int64
	mu             sync.Mutex
	latencies      []float64
}
type capacityModbus struct{ state *capacityState }

func (h *capacityModbus) HandleCoils(*modbus.CoilsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalDataAddress
}
func (h *capacityModbus) HandleDiscreteInputs(*modbus.DiscreteInputsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalDataAddress
}
func (h *capacityModbus) HandleInputRegisters(r *modbus.InputRegistersRequest) ([]uint16, error) {
	return h.HandleHoldingRegisters(&modbus.HoldingRegistersRequest{UnitId: r.UnitId, Addr: r.Addr, Quantity: r.Quantity})
}
func (h *capacityModbus) HandleHoldingRegisters(r *modbus.HoldingRegistersRequest) ([]uint16, error) {
	if r.UnitId < 1 || r.UnitId > 200 {
		return nil, modbus.ErrIllegalDataAddress
	}
	if r.IsWrite && r.Addr == 10 && len(r.Args) == 4 {
		var started uint64
		for _, word := range r.Args {
			started = started<<16 | uint64(word)
		}
		latency := float64(time.Now().UnixNano()-int64(started)) / 1e6
		h.state.mu.Lock()
		h.state.latencies = append(h.state.latencies, latency)
		h.state.mu.Unlock()
		return r.Args, nil
	}
	if r.IsWrite || r.Addr != 0 || r.Quantity != 2 {
		return nil, modbus.ErrIllegalDataAddress
	}
	var sequence uint32
	if h.state.active.Load() {
		sequence = h.state.modbus[r.UnitId].Add(1)
		h.state.modbusProduced.Add(1)
	}
	return []uint16{uint16(sequence >> 16), uint16(sequence)}, nil
}

type capacityNamespace struct {
	*server.MapNamespace
	state *capacityState
}

func (n *capacityNamespace) Attribute(id *ua.NodeID, attribute ua.AttributeID) *ua.DataValue {
	index, err := strconv.Atoi(strings.TrimPrefix(id.StringID(), "sample-"))
	if attribute == ua.AttributeIDValue && err == nil && index >= 0 && index < 100 {
		return &ua.DataValue{EncodingMask: ua.DataValueValue | ua.DataValueStatusCode | ua.DataValueSourceTimestamp, Value: ua.MustVariant(n.state.opcua[index].Load()), Status: ua.StatusOK, SourceTimestamp: time.Now()}
	}
	n.Mu.RLock()
	defer n.Mu.RUnlock()
	return n.MapNamespace.Attribute(id, attribute)
}
func availableAddress() (string, error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return "", e
	}
	address := l.Addr().String()
	e = l.Close()
	return address, e
}

func RunCapacity(parent context.Context, o CapacityOptions) (map[string]any, error) {
	var finalReport map[string]any
	if o.Duration <= 0 || o.Period < time.Millisecond || o.Binary == "" || o.Directory == "" {
		return nil, errors.New("duration, period, binary and directory are required")
	}
	if err := os.MkdirAll(o.Directory, 0700); err != nil {
		return nil, err
	}
	if o.StateStorage == "" {
		o.StateStorage = "memory"
	}
	if o.StateStorage != "memory" && o.StateStorage != "disk" {
		return nil, errors.New("state storage must be memory or disk")
	}
	recorder, e := newSequenceRecorder(uint64((o.Duration+30*time.Second)/o.Period) + 10000)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	state := new(capacityState)
	binary, e := os.ReadFile(o.Binary)
	if e != nil {
		return nil, e
	}
	binaryHash := fmt.Sprintf("%x", sha256.Sum256(binary))
	mbAddress, e := availableAddress()
	if e != nil {
		return nil, e
	}
	mb, e := modbus.NewServer(&modbus.ServerConfiguration{URL: "tcp://" + mbAddress, MaxClients: 1024, Logger: log.New(io.Discard, "", 0)}, &capacityModbus{state})
	if e != nil {
		return nil, e
	}
	if e = mb.Start(); e != nil {
		return nil, e
	}
	defer mb.Stop()
	broker := mqtt.New(&mqtt.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if e = broker.AddHook(new(auth.AllowHook), nil); e != nil {
		return nil, e
	}
	mqttAddress, e := availableAddress()
	if e != nil {
		return nil, e
	}
	if e = broker.AddListener(listeners.NewTCP(listeners.Config{ID: "capacity", Address: mqttAddress})); e != nil {
		return nil, e
	}
	if e = broker.Serve(); e != nil {
		return nil, e
	}
	defer broker.Close()
	publisher := paho.NewClient(paho.NewClientOptions().AddBroker("tcp://" + mqttAddress).SetClientID("capacity-source").SetOrderMatters(false))
	if token := publisher.Connect(); !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		return nil, fmt.Errorf("MQTT source: %v", token.Error())
	}
	defer publisher.Disconnect(100)
	opcAddress, e := availableAddress()
	if e != nil {
		return nil, e
	}
	_, portText, _ := net.SplitHostPort(opcAddress)
	port, _ := strconv.Atoi(portText)
	var namespace int
	var publishOPCUA func(uint32) (int64, error)
	if o.OPCUAPython != "" {
		var closePeer func()
		namespace, publishOPCUA, closePeer, e = startOPCUAPeer(ctx, o.OPCUAPython, o.Directory, port)
		if e != nil {
			return nil, e
		}
		defer closePeer()
	} else {
		opc := server.New(server.EndPoint("127.0.0.1", port), server.EnableSecurity("None", ua.MessageSecurityModeNone), server.EnableAuthMode(ua.UserTokenTypeAnonymous), server.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		base := server.NewMapNamespace(opc, "urn:smartfactory:capacity")
		for i := 0; i < 100; i++ {
			base.Data[fmt.Sprintf("sample-%d", i)] = uint32(0)
		}
		ns := &capacityNamespace{base, state}
		opc.AddNamespace(ns)
		if e = opc.Start(ctx); e != nil {
			return nil, e
		}
		defer opc.Close()
		namespace = int(ns.ID())
		publishOPCUA = func(sequence uint32) (int64, error) {
			for i := 0; i < 100; i++ {
				state.opcua[i].Store(sequence)
				base.ChangeNotification(fmt.Sprintf("sample-%d", i))
			}
			return 100, nil
		}
	}
	grpcAddress, e := availableAddress()
	if e != nil {
		return nil, e
	}
	managementAddress, e := availableAddress()
	if e != nil {
		return nil, e
	}
	cfg := config.Defaults()
	cfg.Environment = config.EnvDevelopment
	cfg.Runtime.StatePath = ":memory:"
	if o.StateStorage == "disk" {
		cfg.Runtime.StatePath = filepath.Join(o.Directory, "datatransfer.db")
	}
	cfg.Runtime.RingSize = 8192
	cfg.GRPC.Addr = grpcAddress
	cfg.Management.Addr = managementAddress
	cfg.Log.Level = "error"
	cfg.Backpressure.Policy = "BP_BLOCK"
	modbusConfig := config.ConnectorConfig{ConnectorID: "capacity-modbus", Protocol: "modbus_tcp", Connection: config.ConnectionConfig{URL: "tcp://" + mbAddress, TimeoutMillis: 1000}, Polling: config.PollingConfig{IntervalMillis: int(o.Period.Milliseconds())}}
	mqttConfig := config.ConnectorConfig{ConnectorID: "capacity-mqtt", Protocol: "mqtt_device", Connection: config.ConnectionConfig{URL: "tcp://" + mqttAddress, MQTTVersion: "5.0", TimeoutMillis: 1000}}
	opcConfig := config.ConnectorConfig{ConnectorID: "capacity-opcua", Protocol: "opcua", Connection: config.ConnectionConfig{URL: "opc.tcp://" + opcAddress, TimeoutMillis: 2000}, Polling: config.PollingConfig{Mode: "subscribe", IntervalMillis: int(o.Period.Milliseconds()), PublishIntervalMillis: max(1, int(o.Period.Milliseconds()/4))}}
	for i := 1; i <= 200; i++ {
		modbusConfig.Devices = append(modbusConfig.Devices, config.DeviceConfig{DeviceID: fmt.Sprintf("modbus-%03d", i), Protocol: "modbus_tcp", UnitID: uint8(i), Datapoints: []config.DatapointConfig{{Key: "sample", RegisterType: "holding_register", DataType: "uint32", Quantity: 2}}, ActionMappings: map[string]config.ActionMapping{"latency": {Type: "write_registers", Address: 10, Quantity: 4, DataType: "uint64", Param: "started_ns"}}})
		mqttConfig.Devices = append(mqttConfig.Devices, config.DeviceConfig{DeviceID: fmt.Sprintf("mqtt-%03d", i), Protocol: "mqtt_device", Datapoints: []config.DatapointConfig{{Key: "sample", Source: "sample", DataType: "uint32"}}})
	}
	for i := 0; i < 100; i++ {
		opcConfig.Devices = append(opcConfig.Devices, config.DeviceConfig{DeviceID: fmt.Sprintf("opcua-%03d", i), Protocol: "opcua", Datapoints: []config.DatapointConfig{{Key: "sample", NodeID: fmt.Sprintf("ns=%d;s=sample-%d", namespace, i), DataType: "uint32"}}})
	}
	for group := 0; group < 20; group++ {
		bus := modbusConfig
		bus.ConnectorID = fmt.Sprintf("capacity-modbus-%02d", group)
		bus.Devices = modbusConfig.Devices[group*10 : (group+1)*10]
		cfg.Connectors = append(cfg.Connectors, bus)
	}
	cfg.Connectors = append(cfg.Connectors, mqttConfig, opcConfig)
	raw, e := yaml.Marshal(cfg)
	if e != nil {
		return nil, e
	}
	configPath := filepath.Join(o.Directory, "datatransfer.yaml")
	if e = os.WriteFile(configPath, raw, 0600); e != nil {
		return nil, e
	}
	logFile, e := os.Create(filepath.Join(o.Directory, "datatransfer.log"))
	if e != nil {
		return nil, e
	}
	defer logFile.Close()
	process := exec.CommandContext(ctx, o.Binary, "-config", configPath)
	process.Stdout = logFile
	process.Stderr = logFile
	if e = process.Start(); e != nil {
		return nil, e
	}
	defer func() {
		_ = process.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = process.Process.Kill()
			<-done
		}
		if finalReport != nil && process.ProcessState != nil {
			if usage, ok := process.ProcessState.SysUsage().(*syscall.Rusage); ok {
				peak := usage.Maxrss
				if runtime.GOOS == "linux" {
					peak *= 1024
				}
				observed := finalReport["maximum_rss_bytes"].(int64)
				finalReport["maximum_rss_bytes"] = max(observed, peak)
				finalReport["rss_pass"] = max(observed, peak) <= 256<<20
				finalReport["os_lifetime_peak_rss_bytes"] = peak
			}
		}
	}()
	connection, e := grpc.NewClient(grpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if e != nil {
		return nil, e
	}
	defer connection.Close()
	client := dt.NewDataTransferServiceClient(connection)
	startupDeadline := time.Now().Add(30 * time.Second)
	for {
		call, done := context.WithTimeout(ctx, time.Second)
		_, e = client.GetMetrics(call, &dt.MetricsRequest{})
		done()
		if e == nil {
			break
		}
		if time.Now().After(startupDeadline) {
			return nil, fmt.Errorf("DataTransfer startup: %w", e)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	stream, e := client.SubscribeTelemetry(ctx, &dt.SubscribeRequest{ConsumerId: "capacity-receiver"})
	if e != nil {
		return nil, e
	}
	var received, duplicate, bad atomic.Int64
	messages := make(chan *dt.DeviceMessage, 4096)
	errors := make(chan error, 4)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer close(messages)
		for {
			m, err := stream.Recv()
			if err != nil {
				if ctx.Err() == nil {
					errors <- err
				}
				return
			}
			select {
			case messages <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-messages:
				if !ok {
					return
				}
				points := msg.GetTelemetry().GetDatapoints()
				if len(points) != 1 || points[0].Quality != dt.DataQuality_GOOD {
					bad.Add(1)
					continue
				}
				var value uint64
				switch v := points[0].GetValue().GetKind().(type) {
				case *dt.DataValue_UintValue:
					value = v.UintValue
				case *dt.DataValue_IntValue:
					if v.IntValue < 0 {
						bad.Add(1)
						continue
					}
					value = uint64(v.IntValue)
				default:
					bad.Add(1)
					continue
				}
				if value == 0 {
					continue
				}
				fresh, err := recorder.add(msg.GetDevice().GetDeviceId(), value)
				if err != nil {
					errors <- err
					return
				}
				if fresh {
					received.Add(1)
				} else {
					duplicate.Add(1)
				}
			}
		}
	}()
	defer func() { cancel(); workers.Wait() }()
	// Readiness includes all actual OPC-UA subscriptions and the MQTT connection.
	warmup := time.NewTimer(o.Warmup)
	warmupTick := time.NewTicker(time.Second)
warming:
	for {
		select {
		case <-ctx.Done():
			warmup.Stop()
			warmupTick.Stop()
			return nil, ctx.Err()
		case <-warmup.C:
			warmupTick.Stop()
			break warming
		case <-warmupTick.C:
			for i := 1; i <= 200; i++ {
				raw, _ := json.Marshal(map[string]any{"timestamp": time.Now().UnixMilli(), "sample": 0})
				token := publisher.Publish(fmt.Sprintf("devices/mqtt-%03d/telemetry", i), 1, false, raw)
				if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
					warmup.Stop()
					warmupTick.Stop()
					return nil, fmt.Errorf("MQTT warmup: %v", token.Error())
				}
			}
		}
	}
	metrics, e := client.GetMetrics(ctx, &dt.MetricsRequest{})
	if e != nil {
		return nil, e
	}
	if metrics.ConnectedDevices < 500 {
		return nil, fmt.Errorf("only %d/500 devices connected after warmup", metrics.ConnectedDevices)
	}
	started := time.Now()
	state.active.Store(true)
	var producer sync.WaitGroup
	producer.Add(1)
	producerCtx, stopProducer := context.WithCancel(ctx)
	defer stopProducer()
	go func() {
		defer producer.Done()
		timer := time.NewTicker(o.Period)
		defer timer.Stop()
		var sequence uint32
		for {
			select {
			case <-producerCtx.Done():
				return
			case <-timer.C:
				sequence++
				tokens := make([]paho.Token, 0, 200)
				for i := 1; i <= 200; i++ {
					payload, _ := json.Marshal(map[string]any{"message_id": fmt.Sprintf("capacity:%d:%d", i, sequence), "timestamp": time.Now().UnixMilli(), "source_sequence": sequence, "sample": sequence})
					tokens = append(tokens, publisher.Publish(fmt.Sprintf("devices/mqtt-%03d/telemetry", i), 1, false, payload))
				}
				for _, token := range tokens {
					if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
						errors <- fmt.Errorf("MQTT publish: %v", token.Error())
						return
					}
					state.mqttProduced.Add(1)
				}
				count, err := publishOPCUA(sequence)
				if err != nil {
					errors <- fmt.Errorf("OPC-UA source: %w", err)
					return
				}
				state.opcuaProduced.Add(count)
			}
		}
	}()
	commandDone := make(chan error, 1)
	go func() {
		interval := min(o.Duration/time.Duration(max(1, o.Commands)), 100*time.Millisecond)
		timer := time.NewTicker(max(interval, time.Millisecond))
		defer timer.Stop()
		for i := 0; i < o.Commands; i++ {
			select {
			case <-ctx.Done():
				commandDone <- ctx.Err()
				return
			case <-timer.C:
			}
			at := time.Now()
			id := fmt.Sprintf("latency:%d:%d", started.UnixNano(), i)
			response, e := client.SendCommand(ctx, &dt.DeviceMessage{MessageId: id, CommandId: id, Timestamp: at.UnixMilli(), Direction: dt.Direction_DOWNSTREAM, Type: dt.MessageType_CONTROL, Device: &dt.DeviceIdentity{DeviceId: fmt.Sprintf("modbus-%03d", i%200+1)}, Payload: &dt.DeviceMessage_Control{Control: &dt.ControlPayload{Action: "latency", Params: map[string]string{"started_ns": strconv.FormatInt(at.UnixNano(), 10)}, Options: &dt.CommandOptions{TimeoutMs: 2000, Idempotent: true, StartDeadlineMs: at.Add(10 * time.Second).UnixMilli()}}}})
			if e != nil {
				commandDone <- e
				return
			}
			if response.Status != dt.CommandStatus_SUCCESS {
				commandDone <- fmt.Errorf("control failed: %s", response.Message)
				return
			}
		}
		commandDone <- nil
	}()
	var maximumRSS int64
	var samples []map[string]any
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	end := time.NewTimer(o.Duration)
	defer end.Stop()
measurement:
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e := <-errors:
			return nil, e
		case <-end.C:
			break measurement
		case <-timer.C:
			rss, e := processRSS(process.Process.Pid)
			if e != nil {
				return nil, e
			}
			maximumRSS = max(maximumRSS, rss)
			sample := map[string]any{"elapsed_seconds": time.Since(started).Seconds(), "rss_bytes": rss, "input": state.modbusProduced.Load() + state.mqttProduced.Load() + state.opcuaProduced.Load(), "stored": received.Load()}
			samples = append(samples, sample)
			if len(samples)%10 == 0 {
				fmt.Printf("elapsed=%.0fs input=%d stored=%d rss=%.1fMiB\n", time.Since(started).Seconds(), sample["input"], sample["stored"], float64(rss)/(1<<20))
			}
		}
	}
	state.active.Store(false)
	stopProducer()
	producer.Wait()
	elapsed := time.Since(started).Seconds()
	select {
	case e = <-commandDone:
		if e != nil {
			return nil, e
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	expected := state.modbusProduced.Load() + state.mqttProduced.Load() + state.opcuaProduced.Load()
	drainUntil := time.Now().Add(30 * time.Second)
	for received.Load() < expected && time.Now().Before(drainUntil) {
		select {
		case e := <-errors:
			return nil, e
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	metrics, e = client.GetMetrics(ctx, &dt.MetricsRequest{})
	if e != nil {
		return nil, e
	}
	state.mu.Lock()
	latencies := append([]float64(nil), state.latencies...)
	state.mu.Unlock()
	sort.Float64s(latencies)
	p99 := 0.0
	if len(latencies) > 0 {
		p99 = latencies[min(len(latencies)-1, int(float64(len(latencies))*.99))]
	}
	expectedSequences := map[string]uint64{}
	for i := 1; i <= 200; i++ {
		expectedSequences[fmt.Sprintf("modbus-%03d", i)] = uint64(state.modbus[i].Load())
		expectedSequences[fmt.Sprintf("mqtt-%03d", i)] = uint64(state.mqttProduced.Load() / 200)
	}
	for i := 0; i < 100; i++ {
		expectedSequences[fmt.Sprintf("opcua-%03d", i)] = uint64(state.opcuaProduced.Load() / 100)
	}
	sequenceReport, stored, exact := recorder.evidence(expectedSequences)
	input := map[string]int64{"modbus": state.modbusProduced.Load(), "mqtt": state.mqttProduced.Load(), "opcua": state.opcuaProduced.Load()}
	valid := exact && received.Load() == expected && bad.Load() == 0 && metrics.ContinuousGapTotal == 0
	rate := float64(expected) / elapsed
	report := map[string]any{"environment": runtime.GOOS + "/" + runtime.GOARCH, "duration_seconds": elapsed, "configured_period_ms": o.Period.Milliseconds(), "warmup_seconds": o.Warmup.Seconds(), "devices": map[string]int{"modbus": 200, "mqtt": 200, "opcua": 100}, "message_grain": "one point per message", "input": input, "stored": stored, "input_total": expected, "stored_total": received.Load(), "duplicate_deliveries": duplicate.Load(), "bad_messages": bad.Load(), "continuous_gap_points": metrics.ContinuousGapTotal, "messages_per_second": rate, "maximum_rss_bytes": maximumRSS, "control_measurements": len(latencies), "control_p99_ms": p99, "counts_match": valid, "throughput_pass": rate >= 5000, "rss_pass": maximumRSS <= 256<<20, "control_pass": len(latencies) >= 1000 && p99 <= 200, "full_duration": o.Duration >= time.Hour, "samples": samples}
	report["recorder"] = "bounded in-memory sequence bitmap"
	report["recorder_memory_bytes"] = recorder.allocated
	report["sequence_evidence"] = sequenceReport
	report["state_storage"] = o.StateStorage
	report["durable_storage_verified"] = false
	report["count_scope"] = "unique telemetry received by the independent validator; application storage is measured separately"
	report["go_version"] = runtime.Version()
	report["logical_cpus"] = runtime.NumCPU()
	report["command"] = os.Args
	report["started_at"] = started.UTC().Format(time.RFC3339Nano)
	report["modbus_connections"] = 20
	report["rss_measurement"] = "one-second samples after warmup and OS lifetime peak"
	report["datatransfer_sha256"] = binaryHash
	report["opcua_peer"] = "gopcua"
	if o.OPCUAPython != "" {
		report["opcua_peer"] = "asyncua"
	}
	if kernel, err := exec.Command("uname", "-srvm").Output(); err == nil {
		report["kernel"] = strings.TrimSpace(string(kernel))
	}
	if revision, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		report["base_revision"] = strings.TrimSpace(string(revision))
	}
	finalReport = report
	return report, nil
}

func processRSS(pid int) (int64, error) {
	if runtime.GOOS == "linux" {
		raw, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if e != nil {
			return 0, e
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				parts := strings.Fields(line)
				n, e := strconv.ParseInt(parts[1], 10, 64)
				return n * 1024, e
			}
		}
		return 0, fmt.Errorf("VmRSS unavailable")
	}
	raw, e := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if e != nil {
		return 0, e
	}
	n, e := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return n * 1024, e
}

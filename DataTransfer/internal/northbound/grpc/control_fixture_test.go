package grpc_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/configmanager"
	"competition2026/product/datatransfer/internal/connector"
	modbus "competition2026/product/datatransfer/internal/connector/modbus"
	adapter "competition2026/product/datatransfer/internal/northbound/grpc"
	runtime "competition2026/product/datatransfer/internal/runtime"
	"competition2026/product/datatransfer/internal/state"
	"google.golang.org/grpc"
)

type countedModbus struct {
	*grpcFakeModbusClient
	current   atomic.Value
	mu        sync.Mutex
	actions   map[string]int
	directory string
}

func (c *countedModbus) WriteRegister(address, value uint16) error {
	if err := c.grpcFakeModbusClient.WriteRegister(address, value); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id, _ := c.current.Load().(string)
	c.actions[id]++
	raw, _ := json.MarshalIndent(map[string]any{"commands": c.actions, "last_write_ms": time.Now().UnixMilli(), "address": address, "value": value}, "", "  ")
	path := filepath.Join(c.directory, "physical-actions.json")
	if err := os.WriteFile(path+".tmp", raw, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// TestControlledControlDeviceFixture uses the actual runtime, persistent command
// journal, generated gRPC service and Modbus connector with a counted simulator.
// It is started by Platform's integration fixture in a separate process.
func TestControlledControlDeviceFixture(t *testing.T) {
	dir := os.Getenv("SF_CONTROL_DEVICE_FIXTURE")
	if dir == "" {
		t.Skip("controlled device fixture directory required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	device := &countedModbus{grpcFakeModbusClient: newGRPCFakeModbusClient(), actions: map[string]int{}, directory: dir}
	connector.Register(modbus.Protocol, func() connector.Connector {
		return modbus.NewConnectorWithClientFactory(func(config.ConnectionConfig) (modbus.Client, error) { return device, nil })
	})
	rt := runtime.New(config.Defaults())
	defer rt.Close()
	journal, err := state.Open(ctx, filepath.Join(dir, "datatransfer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if err = rt.AttachCommandJournal(journal); err != nil {
		t.Fatal(err)
	}
	manager, err := connector.NewManager([]config.ConnectorConfig{grpcModbusConfig()}, rt, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	rt.AttachConnectorManager(manager)
	configuration := configmanager.New(manager, slog.New(slog.NewTextHandler(io.Discard, nil)))
	configuration.SetGlobalApplier(rt)
	rt.AttachConfigManager(configuration)
	if err = configuration.AttachJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	go manager.Start(ctx)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var sendMu sync.Mutex
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if msg, ok := req.(*dt.DeviceMessage); ok && info.FullMethod == dt.DataTransferService_SendCommand_FullMethodName {
			sendMu.Lock()
			defer sendMu.Unlock()
			device.current.Store(msg.CommandId)
			defer device.current.Store("")
		}
		return handler(ctx, req)
	}))
	adapter.Register(server, rt)
	go server.Serve(listener)
	defer server.Stop()
	raw, _ := json.Marshal(map[string]any{"address": listener.Addr().String(), "pid": os.Getpid(), "database": filepath.Join(dir, "datatransfer.db"), "protocol": "actual generated gRPC + Modbus connector with counted register writes"})
	if err = os.WriteFile(filepath.Join(dir, "device-ready.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := os.Stat(filepath.Join(dir, "device-stop")); err == nil {
			return
		}
	}
}

// A loopback process using production ConfigManager, connector Manager and gRPC.
// Its isolated adapter consumes configuration without opening external devices.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/configmanager"
	"competition2026/product/datatransfer/internal/connector"
	adapter "competition2026/product/datatransfer/internal/northbound/grpc"
	runtime "competition2026/product/datatransfer/internal/runtime"
	"competition2026/product/datatransfer/internal/state"
	"google.golang.org/grpc"
)

type consumer struct {
	mu  sync.Mutex
	cfg config.ConnectorConfig
}

func (c *consumer) Init(cfg config.ConnectorConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = cfg
	return nil
}
func (c *consumer) ReloadConfig(cfg config.ConnectorConfig) error { return c.Init(cfg) }
func (c *consumer) Start(ctx context.Context, _ chan<- *dt.DeviceMessage) error {
	<-ctx.Done()
	return nil
}
func (c *consumer) Stop() error { return nil }
func (c *consumer) SendCommand(context.Context, *dt.DeviceMessage) (*dt.CommandResponsePayload, error) {
	return &dt.CommandResponsePayload{Status: dt.CommandStatus_SUCCESS}, nil
}
func (c *consumer) Status() connector.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return connector.Status{ConnectorID: c.cfg.ConnectorID, Protocol: c.cfg.Protocol, State: connector.StateRunning}
}
func (c *consumer) Devices() []*dt.DeviceInfo { return nil }

func registerIsolatedConsumers() {
	for _, protocol := range []string{"mqtt_device", "modbus_tcp", "opcua"} {
		connector.Register(protocol, func() connector.Connector { return &consumer{} })
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	directory := flag.String("directory", "", "private new directory")
	address := flag.String("grpc-listen", "127.0.0.1:0", "loopback gRPC")
	httpAddress := flag.String("http-listen", "127.0.0.1:0", "loopback observations")
	flag.Parse()
	if *directory == "" {
		return fmt.Errorf("directory required")
	}
	if err := os.MkdirAll(*directory, 0700); err != nil {
		return err
	}
	for _, value := range []string{*address, *httpAddress} {
		host, _, e := net.SplitHostPort(value)
		if e != nil || !net.ParseIP(host).IsLoopback() {
			return fmt.Errorf("loopback listeners required")
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	registerIsolatedConsumers()
	rt := runtime.New(config.Defaults())
	defer rt.Close()
	journal, e := state.Open(ctx, filepath.Join(*directory, "datatransfer.db"))
	if e != nil {
		return e
	}
	defer journal.Close()
	manager, e := connector.NewManager([]config.ConnectorConfig{}, rt, nil)
	if e != nil {
		return e
	}
	rt.AttachConnectorManager(manager)
	cm := configmanager.New(manager, nil)
	cm.SetGlobalApplier(rt)
	rt.AttachConfigManager(cm)
	if e = cm.AttachJournal(ctx, journal); e != nil {
		return e
	}
	go manager.Start(ctx)
	listener, e := net.Listen("tcp", *address)
	if e != nil {
		return e
	}
	server := grpc.NewServer()
	adapter.Register(server, rt)
	defer server.Stop()
	go server.Serve(listener)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /runtime", func(w http.ResponseWriter, r *http.Request) {
		rows := []any{}
		for _, c := range manager.ConnectorConfigs() {
			rows = append(rows, map[string]any{"connector_id": c.ConnectorID, "protocol": c.Protocol, "url": c.Connection.URL, "username": c.Connection.Username, "credential_consumed": c.Connection.Password != "", "polling_interval_millis": c.Polling.IntervalMillis, "state": cm.ConnectorConfiguration(c.ConnectorID)})
		}
		json.NewEncoder(w).Encode(map[string]any{"pid": os.Getpid(), "connectors": rows})
	})
	httpListener, e := net.Listen("tcp", *httpAddress)
	if e != nil {
		return e
	}
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	defer httpServer.Close()
	go httpServer.Serve(httpListener)
	raw, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "grpc_address": listener.Addr().String(), "http_address": httpListener.Addr().String()})
	if e = os.WriteFile(filepath.Join(*directory, "ready.json"), raw, 0600); e != nil {
		return e
	}
	<-ctx.Done()
	return nil
}

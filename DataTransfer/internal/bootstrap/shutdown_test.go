package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestAppShutdownWithBlockedGRPCStream(t *testing.T) {
	gate := make(chan struct{})
	connector.Register("shutdown-stream", func() connector.Connector {
		return &shutdownTestConnector{run: func(ctx context.Context, output chan<- *dt.DeviceMessage) error {
			select {
			case <-ctx.Done():
				return nil
			case <-gate:
			}
			payload := strings.Repeat("x", 256<<10)
			tick := time.NewTicker(5 * time.Millisecond)
			defer tick.Stop()
			for i := 0; ; i++ {
				select {
				case <-ctx.Done():
					return nil
				case <-tick.C:
				}
				message := &dt.DeviceMessage{
					MessageId: fmt.Sprintf("shutdown-stream-%d", i), Timestamp: time.Now().UnixMilli(), Type: dt.MessageType_TELEMETRY,
					Device: &dt.DeviceIdentity{DeviceId: "shutdown-device", ConnectorId: "shutdown-test"},
					Payload: &dt.DeviceMessage_Telemetry{Telemetry: &dt.TelemetryPayload{Datapoints: []*dt.Datapoint{
						{Key: "large", Timestamp: time.Now().UnixMilli(), Value: &dt.DataValue{Kind: &dt.DataValue_StringValue{StringValue: payload}}},
					}}},
				}
				select {
				case <-ctx.Done():
					return nil
				case output <- message:
				}
			}
		}}
	})
	address, cancel, done := startShutdownTestApp(t, "shutdown-stream")
	connection, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStaticStreamWindowSize(64<<10), grpc.WithStaticConnWindowSize(64<<10))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	streamContext, stopStream := context.WithTimeout(t.Context(), 10*time.Second)
	defer stopStream()
	stream, err := dt.NewDataTransferServiceClient(connection).SubscribeTelemetry(streamContext, &dt.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	close(gate)
	if _, err := stream.Header(); err != nil {
		t.Fatal(err)
	}
	// No Recv call replenishes the stream window. The next large message waits
	// for transport write quota while the application workers can still stop.
	time.Sleep(100 * time.Millisecond)
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown after %s: %v", time.Since(started), err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("application did not finish its bounded gRPC shutdown")
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("blocked RPC survived application shutdown")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("gRPC listener remained open after shutdown: %v", err)
	}
	listener.Close()
	t.Logf("application stopped after %s with the blocked RPC and listener closed", time.Since(started))
}

func TestAppShutdownReportsUnfinishedWorker(t *testing.T) {
	started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	connector.Register("shutdown-unfinished", func() connector.Connector {
		return &shutdownTestConnector{run: func(ctx context.Context, _ chan<- *dt.DeviceMessage) error {
			defer close(stopped)
			close(started)
			<-ctx.Done()
			<-release
			return nil
		}}
	})
	defer close(release)
	_, cancel, done := startShutdownTestApp(t, "shutdown-unfinished")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("connector did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "worker shutdown") {
			t.Fatalf("unfinished application worker did not retain the shutdown failure: %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("application did not report its worker shutdown deadline")
	}
	t.Cleanup(func() {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("released test connector did not finish")
		}
	})
}

func startShutdownTestApp(t *testing.T, protocol string) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	management, address := freeAddr(t), freeAddr(t)
	file := filepath.Join(t.TempDir(), "datatransfer.yaml")
	data := fmt.Sprintf("management:\n  addr: %q\ngrpc:\n  enabled: true\n  addr: %q\nmqtt:\n  enabled: false\nconnectors:\n  - connector_id: shutdown-test\n    protocol: %q\n", management, address, protocol)
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- (App{ConfigPath: file}).Run(ctx) }()
	waitForHTTPStatus(t, "http://"+management+"/healthz", http.StatusOK)
	return address, cancel, done
}

type shutdownTestConnector struct {
	cfg config.ConnectorConfig
	run func(context.Context, chan<- *dt.DeviceMessage) error
}

func (c *shutdownTestConnector) Init(cfg config.ConnectorConfig) error         { c.cfg = cfg; return nil }
func (c *shutdownTestConnector) ReloadConfig(cfg config.ConnectorConfig) error { return c.Init(cfg) }
func (c *shutdownTestConnector) Start(ctx context.Context, output chan<- *dt.DeviceMessage) error {
	return c.run(ctx, output)
}
func (*shutdownTestConnector) Stop() error { return nil }
func (c *shutdownTestConnector) Status() connector.Status {
	return connector.Status{ConnectorID: c.cfg.ConnectorID, Protocol: c.cfg.Protocol, State: connector.StateRunning}
}
func (*shutdownTestConnector) Devices() []*dt.DeviceInfo { return nil }
func (*shutdownTestConnector) SendCommand(context.Context, *dt.DeviceMessage) (*dt.CommandResponsePayload, error) {
	return nil, nil
}

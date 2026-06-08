package bootstrap

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestAppSmokeStartsManagementAndGRPC(t *testing.T) {
	managementAddr := freeAddr(t)
	grpcAddr := freeAddr(t)
	configPath := filepath.Join(t.TempDir(), "datatransfer.yaml")
	data := []byte(fmt.Sprintf(`
run_mode: embedded
log:
  level: error
management:
  addr: %q
grpc:
  enabled: true
  addr: %q
mqtt:
  enabled: false
runtime:
  ring_size: 16
  command_ttl_seconds: 60
connectors: []
`, managementAddr, grpcAddr))
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (App{ConfigPath: configPath}).Run(ctx)
	}()

	waitForHTTPStatus(t, "http://"+managementAddr+"/healthz", http.StatusOK)
	waitForHTTPStatus(t, "http://"+managementAddr+"/readyz", http.StatusOK)

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	callCtx, callCancel := context.WithTimeout(context.Background(), time.Second)
	defer callCancel()
	if _, err := dtv1.NewDataTransferServiceClient(conn).GetMetrics(callCtx, &dtv1.MetricsRequest{}); err != nil {
		t.Fatalf("GetMetrics returned error: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("App.Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for App.Run shutdown")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free addr: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close free addr listener: %v", err)
	}
	return addr
}

func waitForHTTPStatus(t *testing.T, url string, status int) {
	t.Helper()
	client := http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == status {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not return status %d before timeout", url, status)
}

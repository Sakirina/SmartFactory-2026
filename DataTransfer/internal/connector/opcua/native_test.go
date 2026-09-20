package opcua

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

func TestObservationRetainsProtocolQualityAndTime(t *testing.T) {
	at := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		status  ua.StatusCode
		quality dtv1.DataQuality
	}{
		{ua.StatusOK, dtv1.DataQuality_GOOD},
		{ua.StatusUncertainLastUsableValue, dtv1.DataQuality_UNCERTAIN},
		{ua.StatusBadSensorFailure, dtv1.DataQuality_BAD},
	} {
		t.Run(tc.status.Error(), func(t *testing.T) {
			sample := observation(&ua.DataValue{Value: ua.MustVariant(23.5), SourceTimestamp: at, Status: tc.status})
			point := makeDatapoint(sample, config.DatapointConfig{Key: "temperature", Quality: "good", Unit: "C"})
			if point.Quality != tc.quality || point.Timestamp != at.UnixMilli() || point.TimeSource != "device" || point.GetValue().GetDoubleValue() != 23.5 {
				t.Fatalf("lost protocol sample information: %v", point)
			}
			if tc.quality != dtv1.DataQuality_GOOD && point.QualityReason == "" {
				t.Fatal("missing quality reason")
			}
		})
	}
}

func TestNativeProtocolReadWriteSubscribeAndBadStatus(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	srv := server.New(server.EndPoint("127.0.0.1", port), server.EnableSecurity("None", ua.MessageSecurityModeNone), server.EnableAuthMode(ua.UserTokenTypeAnonymous), server.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	ns := server.NewMapNamespace(srv, "SmartFactory protocol fixture")
	ns.Data["temperature"] = float64(23.5)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		finished := make(chan struct{})
		go func() { _ = srv.Close(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("OPC-UA fixture did not close within one second")
		}
	}()
	client, err := nativeClientFactory(config.ConnectorConfig{Connection: config.ConnectionConfig{URL: fmt.Sprintf("opc.tcp://127.0.0.1:%d", port)}, Polling: config.PollingConfig{IntervalMillis: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	node := fmt.Sprintf("ns=%d;s=temperature", ns.ID())
	raw, err := client.Read(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if got := raw.(Observation); got.Value != float64(23.5) || got.Quality != dtv1.DataQuality_GOOD {
		t.Fatalf("read = %+v", got)
	}
	bad, err := client.Read(ctx, fmt.Sprintf("ns=%d;s=missing", ns.ID()))
	if err != nil {
		t.Fatal(err)
	}
	if bad.(Observation).Quality != dtv1.DataQuality_BAD {
		t.Fatalf("missing node reported GOOD: %+v", bad)
	}
	if err := client.Write(ctx, "ns=0;i=99999999", 9.0); err == nil {
		t.Fatal("unknown node write reported success")
	}

	subCtx, stop := context.WithCancel(ctx)
	defer stop()
	updates := make(chan Observation, 16)
	done := make(chan error, 1)
	go func() {
		done <- client.Subscribe(subCtx, []string{node}, func(_ string, value any) {
			select {
			case updates <- value.(Observation):
			case <-subCtx.Done():
			}
		})
	}()
	select {
	case initial := <-updates:
		if initial.Value != float64(23.5) {
			t.Fatalf("initial value = %+v", initial)
		}
	case err := <-done:
		t.Fatalf("subscription failed: %v", err)
	case <-ctx.Done():
		t.Fatal("subscription did not produce the initial value")
	}
	if err := client.Write(ctx, node, float64(41.5)); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case update := <-updates:
			if update.Value == float64(41.5) {
				stop()
				select {
				case <-done:
					return
				case <-time.After(3 * time.Second):
					t.Fatal("subscription did not stop")
				}
			}
		case err := <-done:
			t.Fatalf("subscription failed: %v", err)
		case <-ctx.Done():
			t.Fatal("subscription did not observe the device write")
		}
	}
}

func TestNativeClientRejectsIncompleteSecurity(t *testing.T) {
	for _, connection := range []config.ConnectionConfig{
		{URL: "opc.tcp://localhost:4840", SecurityMode: "garbage"},
		{URL: "opc.tcp://localhost:4840", SecurityMode: "SignAndEncrypt", SecurityPolicy: "Basic256Sha256"},
		{URL: "opc.tcp://localhost:4840", Username: "operator", Password: "test"},
		{URL: "opc.tcp://localhost:4840", SecurityMode: "None", TLS: config.TLSConfig{Enabled: true}},
	} {
		if _, err := nativeClientFactory(config.ConnectorConfig{Connection: connection}); err == nil {
			t.Fatalf("accepted incomplete security: %+v", connection)
		}
	}
}

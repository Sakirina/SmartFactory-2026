package main

import (
	"competition2026/product/datatransfer/internal/simulator"
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	o := simulator.Options{}
	flag.StringVar(&o.StatePath, "state", ".local/simulator.db", "persistent simulated device state")
	flag.StringVar(&o.ConfigPath, "config-out", ".local/datatransfer-simulator.yaml", "write the corresponding DataTransfer configuration")
	flag.StringVar(&o.Modbus, "modbus", "127.0.0.1:1502", "Modbus TCP listener")
	flag.StringVar(&o.MQTT, "mqtt", "127.0.0.1:18830", "MQTT 3.1.1/5.0 listener")
	flag.StringVar(&o.OPCUA, "opcua", "127.0.0.1:48400", "OPC-UA listener")
	flag.StringVar(&o.HTTP, "http", "127.0.0.1:18083", "simulation scenario API")
	flag.DurationVar(&o.Interval, "interval", 500*time.Millisecond, "telemetry update interval")
	flag.IntVar(&o.FleetCount, "fleet-count", 0, "independent MQTT devices for the site profile; 0 uses the five scenes")
	flag.StringVar(&o.FleetPrefix, "fleet-prefix", "edge-a", "unique device prefix for the site profile")
	flag.DurationVar(&o.CommandReplyDelay, "command-reply-delay", 0, "fleet fixture delay after committing a command, before replying")
	flag.StringVar(&o.GRPC, "gateway-grpc", "127.0.0.1:50051", "generated fleet gateway gRPC listener")
	flag.StringVar(&o.Management, "gateway-http", "127.0.0.1:18082", "generated fleet gateway management listener")
	flag.Parse()
	if o.Interval < time.Millisecond || o.CommandReplyDelay < 0 || o.CommandReplyDelay > 30*time.Second {
		slog.Error("interval must be at least 1ms")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := simulator.Run(ctx, o); e != nil {
		slog.Error("simulator stopped", "error", e)
		os.Exit(1)
	}
}

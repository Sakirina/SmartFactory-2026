package main

import (
	"competition2026/product/datatransfer/internal/acceptance"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	var o acceptance.CapacityOptions
	flag.StringVar(&o.Binary, "datatransfer", "", "absolute path to the DataTransfer executable")
	flag.StringVar(&o.Directory, "directory", "", "new isolated benchmark output directory")
	flag.StringVar(&o.OPCUAPython, "opcua-python", "", "Python executable with asyncua for independent queued OPC-UA peer")
	flag.StringVar(&o.StateStorage, "state-storage", "memory", "DataTransfer state storage: memory for profiling, disk for durable control timing")
	flag.DurationVar(&o.Duration, "duration", time.Hour, "measurement duration")
	flag.DurationVar(&o.Warmup, "warmup", time.Minute, "connection warmup")
	flag.DurationVar(&o.Period, "period", 100*time.Millisecond, "sampling period")
	flag.IntVar(&o.Commands, "commands", 1000, "control measurements")
	flag.Parse()
	if !filepath.IsAbs(o.Binary) || !filepath.IsAbs(o.Directory) {
		fmt.Fprintln(os.Stderr, "binary and directory must be absolute paths")
		os.Exit(2)
	}
	if _, err := os.Stat(filepath.Join(o.Directory, "datatransfer.yaml")); err == nil {
		fmt.Fprintln(os.Stderr, "benchmark directory already contains a dataset")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := acceptance.RunCapacity(ctx, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	path := filepath.Join(o.Directory, "report.json")
	if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Report:", path)
}

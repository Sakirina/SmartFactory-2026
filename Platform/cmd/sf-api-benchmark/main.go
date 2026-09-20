package main

import (
	"competition2026/product/platform/internal/acceptance"
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
	var o acceptance.APIOptions
	flag.StringVar(&o.Directory, "directory", "", "isolated output directory")
	flag.StringVar(&o.StaticDir, "static-dir", "", "frontend build directory")
	flag.StringVar(&o.Listen, "listen", "", "optional loopback listen address")
	flag.DurationVar(&o.Duration, "duration", 2*time.Minute, "concurrent measurement duration")
	flag.IntVar(&o.QueriesPerClient, "queries", 5, "history requests per client")
	flag.BoolVar(&o.Profile, "profile", false, "write a CPU profile during measurement")
	flag.Parse()
	o.DSN = os.Getenv("SF_BENCH_DATABASE")
	if !filepath.IsAbs(o.Directory) {
		fmt.Fprintln(os.Stderr, "absolute --directory required")
		os.Exit(2)
	}
	if _, e := os.Stat(filepath.Join(o.Directory, "report.json")); e == nil {
		fmt.Fprintln(os.Stderr, "report already exists")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, e := acceptance.RunAPI(ctx, o)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	raw, _ := json.MarshalIndent(report, "", "  ")
	if e = os.WriteFile(filepath.Join(o.Directory, "report.json"), append(raw, '\n'), 0600); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	fmt.Println(string(raw))
}

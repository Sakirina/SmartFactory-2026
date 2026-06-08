package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"competition2026/product/datatransfer/internal/bootstrap"
)

func main() {
	configPath := flag.String("config", "", "path to datatransfer YAML config")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := bootstrap.Run(ctx, *configPath); err != nil {
		slog.Error("datatransfer exited with error", "error", err)
		os.Exit(1)
	}
}

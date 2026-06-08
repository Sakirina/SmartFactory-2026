package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"competition2026/product/datatransfer/internal/config"
	grpcadapter "competition2026/product/datatransfer/internal/northbound/grpc"
	mqttadapter "competition2026/product/datatransfer/internal/northbound/mqtt"
	"competition2026/product/datatransfer/internal/observability"
	dtruntime "competition2026/product/datatransfer/internal/runtime"
	"google.golang.org/grpc"
)

type App struct {
	ConfigPath string
}

func (a App) Run(ctx context.Context) error {
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return err
	}
	logger := newLogger(cfg.Log.Level)
	rt := dtruntime.New(cfg)

	errCh := make(chan error, 3)
	httpServer := &http.Server{
		Addr:              cfg.Management.Addr,
		Handler:           observability.Handler(rt),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("management server starting", "addr", cfg.Management.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var grpcServer *grpc.Server
	if cfg.GRPC.Enabled {
		grpcServer = grpc.NewServer()
		grpcadapter.Register(grpcServer, rt)
		listener, err := net.Listen("tcp", cfg.GRPC.Addr)
		if err != nil {
			return err
		}
		rt.SetGRPCServing(true)
		go func() {
			logger.Info("grpc server starting", "addr", cfg.GRPC.Addr)
			if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				errCh <- err
			}
		}()
	}

	if cfg.MQTT.Enabled {
		adapter := mqttadapter.New(cfg.MQTT, rt, logger)
		go func() {
			logger.Info("mqtt adapter starting", "broker", cfg.MQTT.Broker, "gateway_id", cfg.MQTT.GatewayID)
			if err := adapter.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				errCh <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("management server shutdown failed", "error", err)
	}
	if grpcServer != nil {
		rt.SetGRPCServing(false)
		stopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-shutdownCtx.Done():
			grpcServer.Stop()
		}
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var parsed slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn", "warning":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		parsed = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}

func Run(ctx context.Context, configPath string) error {
	if err := (App{ConfigPath: configPath}).Run(ctx); err != nil {
		return fmt.Errorf("datatransfer app failed: %w", err)
	}
	return nil
}

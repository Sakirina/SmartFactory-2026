// Package bootstrap 负责进程装配:加载配置、初始化日志、创建 Runtime 与各模块,
// 按运行模式(embedded/split)启动管理端、gRPC/MQTT 北向与 Connector Manager,
// 并处理信号驱动的优雅停机。gRPC 反射仅在 environment=development 且显式开启时注册。
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

	"competition2026/product/datatransfer/internal/buffer"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/configmanager"
	"competition2026/product/datatransfer/internal/connector"
	_ "competition2026/product/datatransfer/internal/connector/modbus"
	_ "competition2026/product/datatransfer/internal/connector/mqttdevice"
	_ "competition2026/product/datatransfer/internal/connector/opcua"
	grpcadapter "competition2026/product/datatransfer/internal/northbound/grpc"
	mqttadapter "competition2026/product/datatransfer/internal/northbound/mqtt"
	"competition2026/product/datatransfer/internal/observability"
	dtruntime "competition2026/product/datatransfer/internal/runtime"
	"competition2026/product/datatransfer/internal/security"
	"competition2026/product/datatransfer/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
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
	connectorManager, err := connector.NewManager(cfg.Connectors, rt, logger)
	if err != nil {
		return err
	}
	rt.AttachConnectorManager(connectorManager)
	configManager := configmanager.New(connectorManager, logger)
	configManager.SetGlobalApplier(rt)
	rt.AttachConfigManager(configManager)
	if cfg.MQTT.Enabled && cfg.MQTT.TLS.Enabled && cfg.MQTT.TLS.InsecureSkipVerify {
		logger.Warn("mqtt tls certificate verification is DISABLED (insecure_skip_verify); never use this in production")
	}

	var bufferStore *buffer.Store
	if cfg.Buffer.Enabled {
		bufferStore, err = storage.OpenBuffer(ctx, cfg.Buffer)
		if err != nil {
			return err
		}
		defer func() {
			if err := bufferStore.Close(); err != nil {
				logger.Warn("buffer store close failed", "error", err)
			}
		}()
	}

	errCh := make(chan error, 5)
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

	go func() {
		logger.Info("connector manager starting", "connectors", len(cfg.Connectors))
		if err := connectorManager.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errCh <- err
		}
	}()

	var grpcServer *grpc.Server
	if cfg.GRPC.Enabled {
		grpcServer, err = buildGRPCServer(cfg, rt, logger)
		if err != nil {
			return err
		}
		listener, err := net.Listen("tcp", cfg.GRPC.Addr)
		if err != nil {
			return err
		}
		rt.SetGRPCServing(true)
		go func() {
			logger.Info("grpc server starting", "addr", cfg.GRPC.Addr, "tls", cfg.GRPC.TLS.Enabled, "reflection", reflectionEnabled(cfg))
			if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				errCh <- err
			}
		}()
	}

	if cfg.MQTT.Enabled {
		var adapter *mqttadapter.Adapter
		if bufferStore != nil {
			adapter = mqttadapter.New(cfg.MQTT, rt, logger, mqttadapter.WithBuffer(bufferStore, cfg.Buffer))
			rt.AttachUpstreamSink(adapter)
			rt.AttachPersistentBuffer(adapter)
		} else {
			adapter = mqttadapter.New(cfg.MQTT, rt, logger)
			rt.AttachUpstreamSink(adapter)
		}
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

// buildGRPCServer 按配置组装 gRPC 服务端:可选服务端 TLS(含 mTLS),
// 以及仅限开发环境的 Server Reflection(默认关闭;生产配置已被 Validate 拒绝,
// 此处再按 environment 判断一次作为纵深防御)。
func buildGRPCServer(cfg config.Config, rt *dtruntime.Runtime, logger *slog.Logger) (*grpc.Server, error) {
	var opts []grpc.ServerOption
	if cfg.GRPC.TLS.Enabled {
		tlsCfg, err := security.ServerTLSConfig(cfg.GRPC.TLS)
		if err != nil {
			return nil, fmt.Errorf("grpc server tls: %w", err)
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}
	server := grpc.NewServer(opts...)
	grpcadapter.Register(server, rt)
	if reflectionEnabled(cfg) {
		reflection.Register(server)
		logger.Warn("grpc server reflection is ENABLED (development only); do not expose this endpoint to untrusted networks")
	}
	return server, nil
}

func reflectionEnabled(cfg config.Config) bool {
	return cfg.GRPC.Reflection && cfg.Environment == config.EnvDevelopment
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

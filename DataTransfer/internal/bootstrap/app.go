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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"competition2026/product/datatransfer/internal/buffer"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/configmanager"
	"competition2026/product/datatransfer/internal/connector"
	_ "competition2026/product/datatransfer/internal/connector/modbus"
	_ "competition2026/product/datatransfer/internal/connector/mqttdevice"
	_ "competition2026/product/datatransfer/internal/connector/opcua"
	_ "competition2026/product/datatransfer/internal/connector/sidecar"
	grpcadapter "competition2026/product/datatransfer/internal/northbound/grpc"
	mqttadapter "competition2026/product/datatransfer/internal/northbound/mqtt"
	"competition2026/product/datatransfer/internal/observability"
	dtruntime "competition2026/product/datatransfer/internal/runtime"
	"competition2026/product/datatransfer/internal/security"
	"competition2026/product/datatransfer/internal/state"
	"competition2026/product/datatransfer/internal/storage"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
)

type App struct {
	ConfigPath string
}

func (a App) Run(ctx context.Context) (runErr error) {
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	cfg, err := config.Load(a.ConfigPath)
	if err != nil {
		return err
	}
	logger := newLogger(cfg.Log.Level)
	rt := dtruntime.New(cfg)
	defer rt.Close()
	connectorManager, err := connector.NewManager(cfg.Connectors, rt, logger)
	if err != nil {
		return err
	}
	rt.AttachConnectorManager(connectorManager)
	configManager := configmanager.New(connectorManager, logger)
	configManager.SetGlobalApplier(rt)
	rt.AttachConfigManager(configManager)
	statePath := cfg.Runtime.StatePath
	if statePath != ":memory:" && !strings.HasPrefix(statePath, "file:") && !filepath.IsAbs(statePath) && a.ConfigPath != "" {
		statePath = filepath.Join(filepath.Dir(a.ConfigPath), statePath)
	}
	journal, err := state.Open(ctx, statePath)
	if err != nil {
		return fmt.Errorf("open runtime state: %w", err)
	}
	defer journal.Close()
	defer rt.Close()
	if err = rt.AttachCommandJournal(journal); err != nil {
		return err
	}
	if err := configManager.AttachJournal(ctx, journal); err != nil {
		return err
	}
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

	// Bind every listener before starting workers so startup failure leaves no service running.
	httpListener, err := net.Listen("tcp", cfg.Management.Addr)
	if err != nil {
		return err
	}
	defer httpListener.Close()
	var grpcServer *grpc.Server
	var grpcListener net.Listener
	if cfg.GRPC.Enabled {
		grpcServer, err = buildGRPCServer(cfg, rt, logger)
		if err != nil {
			return err
		}
		grpcListener, err = net.Listen("tcp", cfg.GRPC.Addr)
		if err != nil {
			return err
		}
		defer grpcListener.Close()
	}

	errCh := make(chan error, 5)
	var workers sync.WaitGroup
	start := func(fn func() error) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := fn(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, grpc.ErrServerStopped) {
				errCh <- err
			}
		}()
	}
	connections := &httpConnections{states: make(map[net.Conn]http.ConnState)}
	httpServer := &http.Server{
		Addr:              cfg.Management.Addr,
		Handler:           observability.Handler(rt),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ConnState:         connections.track,
	}
	defer func() {
		cancelRun()
		runErr = errors.Join(runErr, rt.Close())
		rt.SetGRPCServing(false)
		connections.stopAcceptingRequests()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped := make(chan struct{})
		go func() {
			if grpcServer != nil {
				grpcServer.GracefulStop()
			}
			close(stopped)
		}()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			runErr = errors.Join(runErr, fmt.Errorf("management shutdown: %w", err))
		}
		select {
		case <-stopped:
		case <-shutdownCtx.Done():
			if grpcServer != nil {
				grpcServer.Stop()
			}
		}
		workersDone := make(chan struct{})
		go func() { workers.Wait(); close(workersDone) }()
		select {
		case <-workersDone:
		case <-shutdownCtx.Done():
			runErr = errors.Join(runErr, fmt.Errorf("worker shutdown: %w", shutdownCtx.Err()))
		}
	}()
	start(func() error {
		logger.Info("management server starting", "addr", cfg.Management.Addr)
		return httpServer.Serve(httpListener)
	})

	start(func() error {
		logger.Info("connector manager starting", "connectors", len(cfg.Connectors))
		return connectorManager.Start(ctx)
	})
	start(func() error {
		timer := time.NewTicker(time.Minute)
		defer timer.Stop()
		for {
			if _, err := journal.CompactAcknowledged(ctx, 500); err != nil && ctx.Err() == nil {
				logger.Warn("acknowledged payload cleanup deferred", "error", err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	})

	if grpcServer != nil {
		rt.SetGRPCServing(true)
		start(func() error {
			logger.Info("grpc server starting", "addr", cfg.GRPC.Addr, "tls", cfg.GRPC.TLS.Enabled, "reflection", reflectionEnabled(cfg))
			return grpcServer.Serve(grpcListener)
		})
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
		start(func() error {
			logger.Info("mqtt adapter starting", "broker", cfg.MQTT.Broker, "gateway_id", cfg.MQTT.GatewayID)
			return adapter.Start(ctx)
		})
	}

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	return nil
}

// net/http waits for new connections to become idle before shutting down. A client
// that connects without sending a request must not delay service cancellation.
type httpConnections struct {
	mu      sync.Mutex
	states  map[net.Conn]http.ConnState
	closing bool
}

func (c *httpConnections) track(conn net.Conn, state http.ConnState) {
	c.mu.Lock()
	if state == http.StateClosed || state == http.StateHijacked {
		delete(c.states, conn)
	} else {
		c.states[conn] = state
	}
	closeNow := c.closing && (state == http.StateNew || state == http.StateIdle)
	c.mu.Unlock()
	if closeNow {
		_ = conn.Close()
	}
}

func (c *httpConnections) stopAcceptingRequests() {
	c.mu.Lock()
	c.closing = true
	var idle []net.Conn
	for conn, state := range c.states {
		if state == http.StateNew || state == http.StateIdle {
			idle = append(idle, conn)
		}
	}
	c.mu.Unlock()
	for _, conn := range idle {
		_ = conn.Close()
	}
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

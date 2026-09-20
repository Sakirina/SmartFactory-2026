package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"competition2026/product/platform/internal/ai"
	"competition2026/product/platform/internal/api"
	"competition2026/product/platform/internal/cloudsync"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/coordination"
	"competition2026/product/platform/internal/datatransfer"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/thingsboard"
	"competition2026/product/platform/pkg/model"
)

type Options struct {
	Mode, NodeID, Address, DSN, KeyFile, ServiceToken, StaticDir, ConfigURL, BootstrapPassword string
	Seed                                                                                       bool
	TBURL, TBUsername, TBPassword, TBCallbackURL                                               string
	DataTransferAddress                                                                        string
	DataTransferCA, DataTransferCertificate, DataTransferKey                                   string
	SyncURL, SyncListen, TLSCA, TLSCertificate, TLSKey, CloudSigningKey                        string
	NativeRelayListen, NativeRelayTarget                                                       string
	NATSURL, NATSToken, NATSPrefix, NATSCA, NATSCertificate, NATSKey                           string
}
type Application struct {
	Options     Options
	Store       *store.Store
	Server      *api.Server
	Native      *thingsboard.Adapter
	Bridge      *datatransfer.Bridge
	SyncServer  *cloudsync.Server
	SyncClient  *cloudsync.Client
	NativeRelay *cloudsync.Relay
	Site        *coordination.Manager
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func Open(ctx context.Context, o Options) (*Application, error) {
	if !oneOf(o.Mode, "cloud", "edge", "config") {
		return nil, errors.New("mode must be cloud, edge or config")
	}
	key, e := masterKey(o.KeyFile)
	if e != nil {
		return nil, e
	}
	s, e := store.Open(ctx, o.DSN, o.NodeID, key)
	if e != nil {
		return nil, e
	}
	auth := &identity.Manager{Store: s, Master: key, Edge: o.Mode == "edge"}
	eng := &engine.Service{Store: s, ControlEnabled: o.Mode == "edge"}
	controls := &control.Service{Store: s, Identity: auth, Definitions: eng, NodeID: o.NodeID, Edge: o.Mode == "edge"}
	cfg := &configcenter.Service{Store: s, Identity: auth}
	srv := &api.Server{Store: s, Identity: auth, Engine: eng, Control: controls, Config: cfg, Mode: o.Mode, NodeID: o.NodeID, ServiceToken: o.ServiceToken, StaticDir: o.StaticDir, ConfigURL: o.ConfigURL}
	a := &Application{Options: o, Store: s, Server: srv}
	if o.SyncURL != "" && o.Mode == "edge" {
		client, err := cloudsync.NewHTTPClient(o.TLSCA, o.TLSCertificate, o.TLSKey)
		if err != nil {
			s.Close()
			return nil, err
		}
		public, err := base64.StdEncoding.DecodeString(o.CloudSigningKey)
		if err != nil || len(public) != ed25519.PublicKeySize {
			s.Close()
			return nil, errors.New("SF_CLOUD_SIGNING_KEY must pin the cloud Ed25519 public key")
		}
		a.SyncClient = &cloudsync.Client{Store: s, Identity: auth, URL: o.SyncURL, HTTP: client, CloudSigningKey: public}
	}
	if o.SyncListen != "" && o.Mode == "cloud" {
		a.SyncServer = &cloudsync.Server{Store: s, Identity: auth}
	}
	if o.NativeRelayListen != "" {
		a.NativeRelay = &cloudsync.Relay{Listen: o.NativeRelayListen, Target: o.NativeRelayTarget}
		if o.Mode == "cloud" {
			authority := &cloudsync.Server{Store: s, Identity: auth}
			a.NativeRelay.ServerTLS, e = authority.TLSConfig(o.TLSCA, o.TLSCertificate, o.TLSKey)
			a.NativeRelay.Admit = func(ctx context.Context, cert *x509.Certificate) error {
				_, _, err := authority.Admit(ctx, cert)
				return err
			}
		} else {
			var client *http.Client
			client, e = cloudsync.NewHTTPClient(o.TLSCA, o.TLSCertificate, o.TLSKey)
			if e == nil {
				a.NativeRelay.ClientTLS = client.Transport.(*http.Transport).TLSClientConfig
			}
		}
		if e != nil {
			s.Close()
			return nil, e
		}
	}
	s.ForwardObservations = o.Mode == "edge"
	if o.Mode == "edge" && o.DataTransferAddress != "" {
		var transport *tls.Config
		if o.DataTransferCA != "" || o.DataTransferCertificate != "" || o.DataTransferKey != "" {
			client, err := cloudsync.NewHTTPClient(o.DataTransferCA, o.DataTransferCertificate, o.DataTransferKey)
			if err != nil {
				s.Close()
				return nil, err
			}
			transport = client.Transport.(*http.Transport).TLSClientConfig
		}
		a.Bridge, e = datatransfer.Open(s, o.NodeID, o.DataTransferAddress, transport)
		if e != nil {
			s.Close()
			return nil, e
		}
		controls.Dispatcher = a.Bridge
	}
	if o.TBURL != "" {
		a.Native = &thingsboard.Adapter{Store: s, Client: &thingsboard.Client{URL: o.TBURL, Username: o.TBUsername, Password: o.TBPassword}, CallbackURL: o.TBCallbackURL, ServiceToken: o.ServiceToken, Edge: o.Mode == "edge"}
	}
	if o.NATSURL != "" && o.Mode == "edge" {
		client, err := cloudsync.NewHTTPClient(o.NATSCA, o.NATSCertificate, o.NATSKey)
		if err != nil {
			s.Close()
			return nil, err
		}
		a.Site = &coordination.Manager{Store: s, Local: controls.Dispatcher, Options: coordination.Options{URL: o.NATSURL, Token: o.NATSToken, Prefix: o.NATSPrefix, TLS: client.Transport.(*http.Transport).TLSClientConfig}}
		controls.Coordinator, controls.Dispatcher = a.Site, a.Site
	}
	toolset := &ai.Tools{API: srv}
	srv.MCP = &ai.MCP{Tools: toolset}
	srv.Chat = &ai.Chat{Tools: toolset, Config: cfg, Default: ai.ModelConfig{Endpoint: os.Getenv("SF_MODEL_ENDPOINT"), Model: os.Getenv("SF_MODEL_NAME"), APIKey: os.Getenv("SF_MODEL_API_KEY"), TimeoutMS: 60000}}
	if users, e := s.List(ctx, "user"); e != nil {
		s.Close()
		return nil, e
	} else if len(users) == 0 {
		if len(o.BootstrapPassword) < 12 {
			s.Close()
			return nil, errors.New("initial SF_BOOTSTRAP_PASSWORD must contain at least 12 characters")
		}
		_, e = auth.CreateUser(ctx, model.Actor{UserID: "bootstrap", Source: "installation"}, model.User{ID: "admin", Login: "admin", Name: "平台管理员", Active: true, Roles: []string{"admin"}, Resources: []string{"*"}, DepartmentID: "engineering"}, o.BootstrapPassword, "", 0)
		if e != nil {
			s.Close()
			return nil, e
		}
	}
	if o.Mode == "config" || o.ConfigURL == "" {
		for _, p := range append(configcenter.Defaults(), configcenter.AdditionalDefaults()...) {
			if _, e := s.Get(ctx, "parameter", p.ID); errors.Is(e, store.ErrNotFound) {
				if _, e = cfg.Put(ctx, model.Actor{UserID: "bootstrap", Source: "installation"}, p, 0); e != nil {
					s.Close()
					return nil, e
				}
			}
		}
	}
	if o.Seed {
		if e = a.Seed(ctx, o.BootstrapPassword); e != nil {
			s.Close()
			return nil, e
		}
	}
	if e = cfg.ApplyPolicy(ctx); e != nil {
		s.Close()
		return nil, e
	}
	return a, nil
}
func masterKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("master key file is required")
	}
	b, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return nil, e
		}
		key := make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return nil, e
		}
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.WriteString(base64.StdEncoding.EncodeToString(key))
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		return key, e
	}
	if e != nil {
		return nil, e
	}
	key, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if e != nil || len(key) != 32 {
		return nil, errors.New("master key must be a base64 encoded 32-byte value")
	}
	return key, nil
}
func (a *Application) Run(ctx context.Context) error {
	ctx, a.cancel = context.WithCancel(ctx)
	defer a.cancel()
	listener, e := net.Listen("tcp", a.Options.Address)
	if e != nil {
		return e
	}
	httpServer := &http.Server{Handler: a.Server.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	errorsCh := make(chan error, 3)
	if a.NativeRelay != nil {
		a.wg.Add(1)
		go func() { defer a.wg.Done(); errorsCh <- a.NativeRelay.Run(ctx) }()
	}
	var syncServer *http.Server
	if a.SyncServer != nil {
		config, err := a.SyncServer.TLSConfig(a.Options.TLSCA, a.Options.TLSCertificate, a.Options.TLSKey)
		if err != nil {
			listener.Close()
			return err
		}
		syncListener, err := net.Listen("tcp", a.Options.SyncListen)
		if err != nil {
			listener.Close()
			return err
		}
		syncServer = &http.Server{Handler: a.SyncServer.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
		go func() { errorsCh <- syncServer.Serve(tls.NewListener(syncListener, config)) }()
	}
	go func() { errorsCh <- httpServer.Serve(listener) }()
	a.startWorkers(ctx)
	slog.Info("SmartFactory service ready", "mode", a.Options.Mode, "node_id", a.Options.NodeID, "address", listener.Addr().String())
	select {
	case <-ctx.Done():
	case e = <-errorsCh:
		if errors.Is(e, http.ErrServerClosed) {
			e = nil
		}
	}
	a.cancel()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
	if syncServer != nil {
		_ = syncServer.Shutdown(shutdown)
	}
	a.wg.Wait()
	return e
}
func (a *Application) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
	if a.Bridge != nil {
		_ = a.Bridge.Close()
	}
	return a.Store.Close()
}
func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func Main(mode string) {
	o := Options{Mode: mode}
	address := "127.0.0.1:8090"
	if mode == "edge" {
		address = "127.0.0.1:8091"
	}
	if mode == "config" {
		address = "127.0.0.1:8092"
	}
	flag.StringVar(&o.Address, "listen", env("SF_LISTEN", address), "HTTP listen address")
	flag.StringVar(&o.NodeID, "node-id", env("SF_NODE_ID", mode+"-1"), "registered node identifier")
	flag.StringVar(&o.DSN, "database", env("SF_DATABASE", ".local/"+mode+".db"), "PostgreSQL URL or SQLite development file")
	flag.StringVar(&o.KeyFile, "master-key", env("SF_MASTER_KEY_FILE", ".local/"+mode+".key"), "encryption and audit signing key file")
	flag.StringVar(&o.StaticDir, "static", env("SF_STATIC_DIR", "../Frontends/dist"), "offline frontend directory")
	flag.StringVar(&o.ConfigURL, "config-url", env("SF_CONFIG_URL", ""), "independent configuration center URL")
	flag.StringVar(&o.DataTransferAddress, "datatransfer", env("SF_DATATRANSFER_ADDRESS", ""), "local DataTransfer gRPC address")
	flag.BoolVar(&o.Seed, "seed", false, "load the editable simulation factory")
	flag.Parse()
	o.ServiceToken = os.Getenv("SF_SERVICE_TOKEN")
	o.DataTransferCA, o.DataTransferCertificate, o.DataTransferKey = os.Getenv("SF_DATATRANSFER_CA"), os.Getenv("SF_DATATRANSFER_CERT"), os.Getenv("SF_DATATRANSFER_KEY")
	o.BootstrapPassword = os.Getenv("SF_BOOTSTRAP_PASSWORD")
	o.TBURL, o.TBUsername, o.TBPassword, o.TBCallbackURL = os.Getenv("SF_TB_URL"), os.Getenv("SF_TB_USERNAME"), os.Getenv("SF_TB_PASSWORD"), os.Getenv("SF_TB_CALLBACK_URL")
	o.SyncURL, o.SyncListen = os.Getenv("SF_SYNC_URL"), os.Getenv("SF_SYNC_LISTEN")
	o.NativeRelayListen, o.NativeRelayTarget = os.Getenv("SF_NATIVE_RELAY_LISTEN"), os.Getenv("SF_NATIVE_RELAY_TARGET")
	o.TLSCA, o.TLSCertificate, o.TLSKey, o.CloudSigningKey = os.Getenv("SF_TLS_CA"), os.Getenv("SF_TLS_CERT"), os.Getenv("SF_TLS_KEY"), os.Getenv("SF_CLOUD_SIGNING_KEY")
	o.NATSURL, o.NATSToken, o.NATSPrefix = os.Getenv("SF_NATS_URL"), os.Getenv("SF_NATS_TOKEN"), os.Getenv("SF_NATS_PREFIX")
	o.NATSCA, o.NATSCertificate, o.NATSKey = os.Getenv("SF_NATS_CA"), os.Getenv("SF_NATS_CERT"), os.Getenv("SF_NATS_KEY")
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a, e := Open(ctx, o)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	defer a.Close()
	if e = a.Run(ctx); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

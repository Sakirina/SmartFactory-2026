package app

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
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
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/tasks"
	"competition2026/product/platform/internal/thingsboard"
	"competition2026/product/platform/pkg/buildinfo"
	"competition2026/product/platform/pkg/model"
)

type Options struct {
	ConfigTokenFile, WorkloadBootstrapFile, ConfigTLSCA, ConfigTLSCertificate, ConfigTLSKey, ConfigAuthorityURL string
	ConfigLegacySubscription                                                                                    bool
	ReleasePayload, ReleaseArtifactRoot                                                                         string
	AuthorityListen, AuthorityTLSCA, AuthorityTLSCertificate, AuthorityTLSKey                                   string
	Mode, NodeID, Address, DSN, KeyFile, ServiceToken, StaticDir, ConfigURL, BootstrapPassword                  string
	Seed                                                                                                        bool
	TBURL, TBUsername, TBPassword, TBCallbackURL                                                                string
	DataTransferAddress                                                                                         string
	DataTransferCA, DataTransferCertificate, DataTransferKey                                                    string
	SyncURL, SyncListen, TLSCA, TLSCertificate, TLSKey, CloudSigningKey                                         string
	NativeRelayListen, NativeRelayTarget                                                                        string
	NATSURL, NATSToken, NATSPrefix, NATSCA, NATSCertificate, NATSKey                                            string
}
type Application struct {
	projectionMu    sync.Mutex
	projectionReady bool
	Tasks           *tasks.Service
	Options         Options
	Store           *store.Store
	Server          *api.Server
	Native          *thingsboard.Adapter
	Bridge          *datatransfer.Bridge
	SyncServer      *cloudsync.Server
	SyncClient      *cloudsync.Client
	NativeRelay     *cloudsync.Relay
	Site            *coordination.Manager
	cancel          context.CancelFunc
	wg              sync.WaitGroup
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
	if o.Mode == "config" {
		cfg.Workloads = &nodeidentity.Service{Store: s, Cipher: auth}
	}
	srv := &api.Server{Store: s, Identity: auth, Engine: eng, Control: controls, Config: cfg, Mode: o.Mode, NodeID: o.NodeID, ServiceToken: o.ServiceToken, StaticDir: o.StaticDir, ConfigURL: o.ConfigURL, ReleaseArtifactRoot: o.ReleaseArtifactRoot}
	hist := srv.HistoryApplication()
	eng.ObserveAnalysis = hist.Observe
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
	srv.Chat = &ai.Chat{Tools: toolset, Config: cfg, Investigations: srv.InvestigationApplication(), Default: ai.ModelConfig{Provider: os.Getenv("SF_MODEL_PROVIDER"), API: os.Getenv("SF_MODEL_API"), Endpoint: os.Getenv("SF_MODEL_ENDPOINT"), Model: os.Getenv("SF_MODEL_NAME"), APIKey: os.Getenv("SF_MODEL_API_KEY"), TimeoutMS: 60000}}
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
	// Release processes obtain fixed configuration from their verified payload.
	// Missing local parameters may have been removed by an earlier release;
	// recreating authority defaults would reuse their immutable versions.
	if o.Mode == "config" || o.ConfigURL == "" && o.ReleasePayload == "" {
		if e = cfg.UpgradeModelSchema(ctx); e != nil {
			s.Close()
			return nil, e
		}
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
	prepared, e := eng.PreparePublishedPlans(ctx)
	if e != nil {
		s.Close()
		return nil, e
	}
	if len(prepared.Isolated) > 0 {
		slog.Warn("legacy rule versions isolated during plan preparation", "count", len(prepared.Isolated))
	}
	if e = a.InitializeNodeConfiguration(ctx); e != nil {
		s.Close()
		return nil, e
	}
	if e = a.initializeRelease(ctx); e != nil {
		s.Close()
		return nil, e
	}
	return a, nil
}
func masterKey(path string) ([]byte, error) { return identity.LoadMasterKey(path) }

func (a *Application) Run(ctx context.Context) error {
	telemetryConfig, telemetryErr := observability.ConfigFromEnv("sf-" + a.Options.Mode)
	if telemetryErr != nil {
		return telemetryErr
	}
	telemetryConfig.NodeID = a.Options.NodeID
	telemetry, telemetryErr := observability.New(ctx, telemetryConfig)
	if telemetryErr != nil {
		return telemetryErr
	}
	telemetry.Install()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetry.Shutdown(shutdown); err != nil {
			slog.Warn("telemetry shutdown failed", "error", err)
		}
	}()
	ctx, a.cancel = context.WithCancel(ctx)
	defer a.cancel()
	listener, e := net.Listen("tcp", a.Options.Address)
	if e != nil {
		return e
	}
	httpServer := &http.Server{Handler: observability.HTTPContext(a.Server.Handler()), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	errorsCh := make(chan error, 4)
	authorityServer, authorityErr := a.startAuthority(ctx, errorsCh)
	if authorityErr != nil {
		listener.Close()
		return authorityErr
	}
	if authorityServer != nil {
		defer authorityServer.Close()
	}
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
	go func() { errorsCh <- a.ServeNodeConfiguration(httpServer, listener) }()
	if e = a.startWorkers(ctx); e != nil {
		_ = httpServer.Close()
		if syncServer != nil {
			_ = syncServer.Close()
		}
		return e
	}
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
	if a.Tasks != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.Tasks.Stop(stopCtx)
		cancel()
	}
	if authorityServer != nil {
		_ = authorityServer.Shutdown(shutdown)
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
	printBuild := flag.Bool("build-info", false, "print executable build identity and exit")
	flag.Parse()
	if *printBuild {
		sha, err := buildinfo.ExecutableSHA256()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"build": buildinfo.Current(mode), "sha256": sha})
		return
	}
	o.ServiceToken = os.Getenv("SF_SERVICE_TOKEN")
	o.ConfigTokenFile, o.WorkloadBootstrapFile = os.Getenv("SF_CONFIG_TOKEN_FILE"), os.Getenv("SF_WORKLOAD_BOOTSTRAP_FILE")
	o.ConfigTLSCA, o.ConfigTLSCertificate, o.ConfigTLSKey = os.Getenv("SF_CONFIG_TLS_CA"), os.Getenv("SF_CONFIG_TLS_CERT"), os.Getenv("SF_CONFIG_TLS_KEY")
	o.ConfigAuthorityURL = os.Getenv("SF_CONFIG_AUTHORITY_URL")
	o.AuthorityListen, o.AuthorityTLSCA, o.AuthorityTLSCertificate, o.AuthorityTLSKey = os.Getenv("SF_AUTHORITY_LISTEN"), os.Getenv("SF_AUTHORITY_TLS_CA"), os.Getenv("SF_AUTHORITY_TLS_CERT"), os.Getenv("SF_AUTHORITY_TLS_KEY")
	o.ConfigLegacySubscription = os.Getenv("SF_CONFIG_LEGACY_SUBSCRIPTION") == "true"
	o.ReleasePayload, o.ReleaseArtifactRoot = os.Getenv("SF_RELEASE_PAYLOAD"), os.Getenv("SF_RELEASE_ARTIFACT_ROOT")
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

// sf-node-configuration-fixture drives the production subscriber in isolated processes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"competition2026/product/platform/internal/app"
	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/cloudsync"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/datatransfer"
	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type settings struct {
	Role                string                                        `json:"role"`
	Directory           string                                        `json:"directory"`
	NodeID              string                                        `json:"node_id"`
	Address             string                                        `json:"address"`
	ConfigURL           string                                        `json:"config_url"`
	AuthorityURL        string                                        `json:"authority_url"`
	TLSCA               string                                        `json:"tls_ca"`
	TLSCertificate      string                                        `json:"tls_certificate"`
	TLSKey              string                                        `json:"tls_key"`
	Password            string                                        `json:"password"`
	TokenFile           string                                        `json:"token_file"`
	RejectFile          string                                        `json:"reject_file"`
	DataTransferAddress string                                        `json:"datatransfer_address"`
	Parameters          []configcenter.Parameter                      `json:"parameters"`
	Connectors          []application.SaveConnectorConfigurationInput `json:"connectors"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("settings", "", "private fixture settings file")
	flag.Parse()
	if *path == "" {
		return errors.New("private settings file required")
	}
	raw, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var opts settings
	if err = store.DecodeJSON(raw, &opts); err != nil {
		return err
	}
	if opts.Directory == "" || opts.NodeID == "" || opts.Address == "" {
		return errors.New("directory, node and loopback address required")
	}
	host, _, err := net.SplitHostPort(opts.Address)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("fixture address must be loopback")
	}
	if err = os.MkdirAll(opts.Directory, 0700); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if opts.Role == "center" {
		a, err := app.Open(ctx, app.Options{Mode: "config", NodeID: opts.NodeID, Address: opts.Address, DSN: filepath.Join(opts.Directory, "configuration.db"), KeyFile: filepath.Join(opts.Directory, "master.key"), BootstrapPassword: opts.Password, ConfigTLSCA: opts.TLSCA, ConfigTLSCertificate: opts.TLSCertificate, ConfigTLSKey: opts.TLSKey, ConfigAuthorityURL: opts.AuthorityURL})
		if err != nil {
			return err
		}
		defer a.Close()
		if _, e := os.Stat(filepath.Join(opts.Directory, ".fixture-seeded")); errors.Is(e, os.ErrNotExist) {
			for _, p := range opts.Parameters {
				expected := int64(0)
				if d, e := a.Store.Get(ctx, "parameter", p.ID); e == nil {
					previous, e := store.Decode[configcenter.Parameter](d)
					if e != nil {
						return e
					}
					expected = previous.Version
				} else if !errors.Is(e, store.ErrNotFound) {
					return e
				}
				if _, e = a.Server.Config.Put(ctx, model.Actor{UserID: "fixture-seed", Source: "installation"}, p, expected); e != nil {
					return e
				}
			}
			if e = os.WriteFile(filepath.Join(opts.Directory, ".fixture-seeded"), []byte("seeded\n"), 0600); e != nil {
				return e
			}
		}
		if len(opts.Connectors) > 0 {
			_, principal, err := a.Server.Identity.Login(ctx, "admin", opts.Password, "", false, "fixture-seed")
			if err != nil {
				return err
			}
			for _, c := range opts.Connectors {
				for _, e := range []model.Entity{{ID: c.GroupID, Name: "配置验证工厂", Kind: "asset", Status: "approved", Version: 1}, {ID: c.EdgeID, Name: "配置验证节点", Kind: "edge", ParentID: c.GroupID, Status: "active", Version: 1}} {
					if _, err = a.Store.Get(ctx, "entity", e.ID); errors.Is(err, store.ErrNotFound) {
						if _, err = a.Store.Put(ctx, "entity", e.ID, 0, e); err != nil {
							return err
						}
					}
				}
				b := &application.Business{Store: a.Store, Identity: a.Server.Identity, Mode: "edge", NodeID: c.EdgeID}
				if _, err = a.Store.Get(ctx, "connector_configuration", c.EdgeID+"/"+c.Parameters.ConnectorID); errors.Is(err, store.ErrNotFound) {
					if _, err = b.SaveConnectorConfiguration(ctx, principal, c); err != nil {
						return err
					}
				}
			}
		}
		return a.Run(ctx)
	}
	if opts.Role != "subscriber" {
		return errors.New("role must be center or subscriber")
	}
	db, err := store.Open(ctx, filepath.Join(opts.Directory, "node.db"), opts.NodeID, make([]byte, 32))
	if err != nil {
		return err
	}
	defer db.Close()
	local := &configcenter.Service{Store: db, Identity: &identity.Manager{Store: db, Master: make([]byte, 32)}}
	if err = local.ApplyPolicy(ctx); err != nil {
		return err
	}
	token, err := os.ReadFile(opts.TokenFile)
	if err != nil {
		return err
	}
	subscriber := &configcenter.Subscriber{URL: opts.ConfigURL, WorkloadToken: strings.TrimSpace(string(token)), NodeID: opts.NodeID, Local: local, InstanceID: fmt.Sprintf("process-%d", time.Now().UnixNano())}
	if strings.HasPrefix(opts.ConfigURL, "https://") {
		client, err := cloudsync.NewHTTPClient(opts.TLSCA, opts.TLSCertificate, opts.TLSKey)
		if err != nil {
			return err
		}
		client.Timeout = 0
		subscriber.Client = client
	}
	subscriber.OnApply = func(ctx context.Context) error {
		if err := local.ApplyPolicy(ctx); err != nil {
			return err
		}
		if opts.RejectFile != "" {
			if _, err := os.Stat(opts.RejectFile); err == nil {
				return errors.New("isolated consumer rejected update after policy application")
			}
		}
		return nil
	}
	subscriber.ApplyConnector = func(ctx context.Context, c model.ConnectorConfiguration, raw json.RawMessage) error {
		result, err := deviceconfig.Validate(deviceconfig.Request{Protocol: c.Protocol, Config: raw})
		if err != nil || !result.Valid {
			return errors.New("isolated connector consumer rejected schema")
		}
		public, err := deviceconfig.PublicConfiguration(result)
		if err != nil {
			return err
		}
		return db.Write(ctx, func(tx *store.Tx) error {
			return tx.SetEphemeral("fixture_connector_consumer", c.ID, map[string]any{"version": c.Version, "public_config": public.Config, "credential_consumed": result.Parameters.Connection["password"] != nil})
		})
	}
	if opts.DataTransferAddress != "" {
		bridge, err := datatransfer.Open(db, opts.NodeID, opts.DataTransferAddress, nil)
		if err != nil {
			return err
		}
		defer bridge.Close()
		subscriber.ApplyConnector = bridge.ApplyConnectorConfiguration
		local.ReadConnectorRuntime = bridge.ReadConnectorRuntime
		local.RemoveConnectorRuntime = bridge.RemoveReleaseConnectorConfiguration
	}
	done := make(chan error, 1)
	go func() { done <- subscriber.Run(ctx) }()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /runtime", func(w http.ResponseWriter, r *http.Request) {
		values, err := local.RuntimeSnapshot(r.Context())
		if err != nil {
			http.Error(w, "runtime unavailable", 500)
			return
		}
		connectors, _ := db.List(r.Context(), "fixture_connector_consumer")
		configuration := []any{}
		for _, d := range connectors {
			var v any
			_ = store.DecodeJSON(d.Data, &v)
			configuration = append(configuration, v)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pid": os.Getpid(), "node_id": opts.NodeID, "instance_id": subscriber.InstanceID, "policy": db.Policy(), "configurations": values, "connectors": configuration})
	})
	server := &http.Server{Addr: opts.Address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serve := make(chan error, 1)
	go func() { serve <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err = <-serve:
		if !errors.Is(err, http.ErrServerClosed) {
			cancel()
			return err
		}
	case err = <-done:
		if err != nil {
			cancel()
			return err
		}
	}
	cancel()
	shutdown, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	_ = server.Shutdown(shutdown)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return errors.New("subscriber did not stop")
	}
	return nil
}

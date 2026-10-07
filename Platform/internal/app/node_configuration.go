package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"

	"competition2026/product/platform/internal/cloudsync"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type WorkloadBootstrap struct {
	Identity   model.WorkloadIdentity `json:"identity"`
	Credential string                 `json:"credential"`
}

func (a *Application) InitializeNodeConfiguration(ctx context.Context) error {
	if a.Bridge != nil {
		a.Server.Config.ReadConnectorRuntime = a.Bridge.ReadConnectorRuntime
		a.Server.Config.RemoveConnectorRuntime = a.Bridge.RemoveReleaseConnectorConfiguration
	}
	if a.Options.ConfigURL != "" && strings.HasPrefix(a.Options.ConfigURL, "https://") {
		client, err := cloudsync.NewHTTPClient(a.Options.ConfigTLSCA, a.Options.ConfigTLSCertificate, a.Options.ConfigTLSKey)
		if err != nil {
			return err
		}
		a.Server.HTTPClient = client
	}
	if a.Options.Mode != "config" && a.Options.Mode != "cloud" && (a.Options.Mode != "edge" || a.Options.ConfigAuthorityURL == "") {
		return nil
	}
	service := &nodeidentity.Service{Store: a.Store, Cipher: a.Server.Identity}
	if (a.Options.Mode == "config" || a.Options.Mode == "edge") && a.Options.ConfigAuthorityURL != "" {
		client, err := cloudsync.NewHTTPClient(a.Options.ConfigTLSCA, a.Options.ConfigTLSCertificate, a.Options.ConfigTLSKey)
		if err != nil {
			return err
		}
		service.AuthorityURL = a.Options.ConfigAuthorityURL
		service.HTTPClient = client
	}
	a.Server.Config.Workloads = service
	if a.Options.Mode == "edge" {
		a.Server.Config.OwnedNodeID = a.Options.NodeID
	}
	a.Server.Config.LegacySubscription = a.Options.ConfigLegacySubscription
	if service.AuthorityURL != "" {
		return nil
	}
	if a.Options.WorkloadBootstrapFile == "" {
		return nil
	}
	info, err := os.Stat(a.Options.WorkloadBootstrapFile)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("workload bootstrap must be private to its owner")
	}
	raw, err := os.ReadFile(a.Options.WorkloadBootstrapFile)
	if err != nil {
		return err
	}
	var items []WorkloadBootstrap
	if err = store.DecodeJSON(raw, &items); err != nil {
		return errors.New("invalid workload bootstrap")
	}
	for _, item := range items {
		if old, e := service.Lookup(ctx, item.Identity.ID); e == nil {
			// Installation material is only consumed on first creation. Existing
			// identities retain later operator scope, rotation and revocation.
			if old.Generation < 1 {
				return errors.New("existing workload has no credential")
			}
			continue
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if err = a.Store.Write(ctx, func(tx *store.Tx) error {
			w, e := service.PutTx(tx, model.Actor{UserID: "installation", Source: "installation"}, item.Identity, 0)
			if e != nil {
				return e
			}
			_, e = service.RotateTx(tx, model.Actor{UserID: "installation", Source: "installation"}, w.ID, item.Credential, w.Version)
			return e
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *Application) StartNodeConfiguration(ctx context.Context) error {
	if a.Options.ReleasePayload != "" {
		return nil
	}
	if a.Options.ConfigURL == "" {
		return nil
	}
	subscriber := &configcenter.Subscriber{URL: a.Options.ConfigURL, NodeID: a.Options.NodeID, Local: a.Server.Config, OnApply: a.applyPolicy}
	if a.Options.ConfigTokenFile != "" {
		info, err := os.Stat(a.Options.ConfigTokenFile)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0077 != 0 {
			return errors.New("workload credential file must be private to its owner")
		}
		raw, err := os.ReadFile(a.Options.ConfigTokenFile)
		if err != nil {
			return err
		}
		subscriber.WorkloadToken = strings.TrimSpace(string(raw))
		if strings.HasPrefix(a.Options.ConfigURL, "https://") {
			client, err := cloudsync.NewHTTPClient(a.Options.ConfigTLSCA, a.Options.ConfigTLSCertificate, a.Options.ConfigTLSKey)
			if err != nil {
				return err
			}
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			client.Timeout = 0 // the SSE stream has no fixed total lifetime
			subscriber.Client = client
		}
		if a.Bridge != nil {
			subscriber.ApplyConnector = a.Bridge.ApplyConnectorConfiguration
		}
	} else {
		if !a.Options.ConfigLegacySubscription {
			return errors.New("independent configuration requires a workload credential file")
		}
		subscriber.Token = a.Options.ServiceToken
	}
	a.wg.Add(1)
	go func() { defer a.wg.Done(); _ = subscriber.Run(ctx) }()
	return nil
}

func (a *Application) ServeNodeConfiguration(server *http.Server, listener net.Listener) error {
	if a.Options.Mode != "config" || a.Options.ConfigTLSCertificate == "" {
		return server.Serve(listener)
	}
	roots, err := cloudsync.RootCAs(a.Options.ConfigTLSCA)
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(a.Options.ConfigTLSCertificate, a.Options.ConfigTLSKey)
	if err != nil {
		return err
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, SessionTicketsDisabled: true}
	return server.Serve(tls.NewListener(listener, config))
}

// ApplyNodeReleaseConfiguration runs fixed configuration at program startup.
func (a *Application) ApplyNodeReleaseConfiguration(ctx context.Context, envelopes []model.ConfigurationEnvelope, private map[string]json.RawMessage) error {
	if err := a.Server.Config.PinLocalRelease(ctx, envelopes); err != nil {
		return err
	}
	subscriber := configcenter.Subscriber{NodeID: a.Options.NodeID, Local: a.Server.Config, OnApply: a.applyPolicy}
	if a.Bridge != nil {
		subscriber.ApplyConnector = a.Bridge.ApplyReleaseConnectorConfiguration
	}
	return subscriber.ApplyEnvelopes(ctx, envelopes, private, true)
}

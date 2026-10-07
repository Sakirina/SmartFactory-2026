package app

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"competition2026/product/platform/internal/cloudsync"
)

func (a *Application) startAuthority(ctx context.Context, errorsCh chan<- error) (*http.Server, error) {
	if (a.Options.Mode != "cloud" && a.Options.Mode != "edge") || a.Options.AuthorityListen == "" {
		return nil, nil
	}
	roots, err := cloudsync.RootCAs(a.Options.AuthorityTLSCA)
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(a.Options.AuthorityTLSCertificate, a.Options.AuthorityTLSKey)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", a.Options.AuthorityListen)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: a.Server.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second}
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, SessionTicketsDisabled: true}
	a.wg.Add(1)
	go func() { defer a.wg.Done(); errorsCh <- server.Serve(tls.NewListener(listener, config)) }()
	return server, nil
}

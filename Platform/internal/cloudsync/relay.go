package cloudsync

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Relay protects ThingsBoard Edge gRPC with node mTLS admission. A cleartext
// listener is permitted only on the same host's loopback interface.
type Relay struct {
	Listen, Target       string
	ServerTLS, ClientTLS *tls.Config
	Admit                func(context.Context, *x509.Certificate) error
}

func (r *Relay) Run(ctx context.Context) error {
	if r.ServerTLS == nil {
		host, _, e := net.SplitHostPort(r.Listen)
		if e != nil {
			return e
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("cleartext native relay listener must use loopback")
		}
		if r.ClientTLS == nil {
			return errors.New("native relay requires TLS on its outbound connection")
		}
	}
	listener, e := net.Listen("tcp", r.Listen)
	if e != nil {
		return e
	}
	if r.ServerTLS != nil {
		listener = tls.NewListener(listener, r.ServerTLS)
	}
	defer listener.Close()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		connection, e := listener.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := r.forward(ctx, connection); e != nil && ctx.Err() == nil {
				slog.Warn("native relay connection ended", "error", e)
			}
		}()
	}
}
func (r *Relay) forward(ctx context.Context, source net.Conn) error {
	defer source.Close()
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cert *x509.Certificate
	if secured, ok := source.(*tls.Conn); ok {
		if e := secured.HandshakeContext(call); e != nil {
			return e
		}
		state := secured.ConnectionState()
		if len(state.PeerCertificates) == 0 || r.Admit == nil {
			return errors.New("native relay requires node admission")
		}
		cert = state.PeerCertificates[0]
		if e := r.Admit(call, cert); e != nil {
			return e
		}
	}
	var destination net.Conn
	var e error
	dialer := net.Dialer{Timeout: 10 * time.Second}
	if r.ClientTLS != nil {
		destination, e = (&tls.Dialer{NetDialer: &dialer, Config: r.ClientTLS}).DialContext(call, "tcp", r.Target)
	} else {
		destination, e = dialer.DialContext(call, "tcp", r.Target)
	}
	if e != nil {
		return e
	}
	defer destination.Close()
	transfer, stop := context.WithCancel(ctx)
	defer stop()
	closeConnections := context.AfterFunc(transfer, func() { source.Close(); destination.Close() })
	defer closeConnections()
	copied := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(destination, source); stop(); copied <- struct{}{} }()
	go func() { _, _ = io.Copy(source, destination); stop(); copied <- struct{}{} }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-transfer.Done():
			<-copied
			<-copied
			return nil
		case <-ticker.C:
			if cert != nil {
				check, done := context.WithTimeout(transfer, 3*time.Second)
				err := r.Admit(check, cert)
				done()
				if err != nil {
					stop()
					<-copied
					<-copied
					return err
				}
			}
		}
	}
}

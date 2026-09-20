package cloudsync

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNativeRelayCertificateAdmissionAndLiveRevocation(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	go func() {
		for {
			conn, e := target.Accept()
			if e != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	probe, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	listen := probe.Addr().String()
	probe.Close()
	cfg, e := f.server.TLSConfig(f.ca, f.serverCert, f.serverKey)
	if e != nil {
		t.Fatal(e)
	}
	relay := &Relay{Listen: listen, Target: target.Addr().String(), ServerTLS: cfg, Admit: func(ctx context.Context, cert *x509.Certificate) error {
		_, _, e := f.server.Admit(ctx, cert)
		return e
	}}
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	clientTLS := f.client.HTTP.Transport.(*http.Transport).TLSClientConfig.Clone()
	var connection *tls.Conn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		connection, e = tls.Dial("tcp", listen, clientTLS)
		if e == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e != nil {
		t.Fatal(e)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(3 * time.Second))
	if _, e = connection.Write([]byte("frame")); e != nil {
		t.Fatal(e)
	}
	reply := make([]byte, 5)
	if _, e = io.ReadFull(connection, reply); e != nil || string(reply) != "frame" {
		t.Fatal("native relay round trip", string(reply), e)
	}
	f.entity.Status = "disabled"
	f.entity.Version++
	if _, e = f.cloud.Put(ctx, "entity", f.entity.ID, 1, f.entity); e != nil {
		t.Fatal(e)
	}
	connection.SetReadDeadline(time.Now().Add(4 * time.Second))
	if _, e = connection.Read(make([]byte, 1)); e == nil {
		t.Fatal("disabled node retained native relay stream")
	}
	cancel()
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not stop")
	}
}

func TestNativeRelayRejectsPublicCleartextListener(t *testing.T) {
	if e := (&Relay{Listen: "0.0.0.0:7071", ClientTLS: &tls.Config{MinVersion: tls.VersionTLS13}}).Run(context.Background()); e == nil {
		t.Fatal("public plaintext relay accepted")
	}
}

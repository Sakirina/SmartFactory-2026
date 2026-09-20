package opcua

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	"competition2026/product/datatransfer/internal/connector"
	"github.com/gopcua/opcua/server"
	"github.com/gopcua/opcua/ua"
)

func unusedPort(t *testing.T) int {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}
func TestNativeSignedEncryptedMethodAndCertificateValidation(t *testing.T) {
	python := os.Getenv("SF_OPCUA_PYTHON")
	if python == "" {
		t.Skip("set SF_OPCUA_PYTHON to the interpreter with tests/fixtures/requirements-opcua.txt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	caKey, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "OPC simulation CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, e := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if e != nil {
		t.Fatal(e)
	}
	caFile := filepath.Join(dir, "ca.pem")
	if e = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); e != nil {
		t.Fatal(e)
	}
	issue := func(name string) ([]byte, *rsa.PrivateKey, string, string) {
		t.Helper()
		key, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			t.Fatal(e)
		}
		uri, _ := url.Parse("urn:smartfactory:" + name)
		cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, e := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
		if e != nil {
			t.Fatal(e)
		}
		certFile, keyFile := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key")
		if e = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); e != nil {
			t.Fatal(e)
		}
		return der, key, certFile, keyFile
	}
	der, _, serverCert, serverKey := issue("server")
	_, _, clientCert, clientKey := issue("client")
	port := unusedPort(t)
	fixturePath, e := filepath.Abs("../../../../tests/fixtures/opcua_secure.py")
	if e != nil {
		t.Fatal(e)
	}
	process := exec.CommandContext(ctx, python, fixturePath, "--port", strconv.Itoa(port), "--certificate", serverCert, "--key", serverKey, "--client-certificate", clientCert)
	process.Stderr = os.Stderr
	if e = process.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { process.Process.Kill(); process.Wait() }()
	waitListening(t, port, ctx)
	endpoint := fmt.Sprintf("opc.tcp://127.0.0.1:%d", port)
	client, e := nativeClientFactory(config.ConnectorConfig{Connection: config.ConnectionConfig{URL: endpoint, SecurityMode: "SignAndEncrypt", SecurityPolicy: "Basic256Sha256", CAFile: caFile, CertFile: clientCert, KeyFile: clientKey, TimeoutMillis: 3000}})
	if e != nil {
		t.Fatal(e)
	}
	if e = client.Connect(ctx); e != nil {
		t.Fatal(e)
	}
	defer client.Close(context.Background())
	result, e := client.Call(ctx, "ns=2;s=gas", "ns=2;s=set_extractor", []any{true})
	if e != nil || len(result) != 1 || result[0] != true {
		t.Fatal("signed method call", result, e)
	}
	value, e := client.Read(ctx, "ns=2;s=extractor")
	if e != nil || value.(Observation).Value != true {
		t.Fatal("method did not change subsequent reads", value, e)
	}
	if _, e = client.Call(ctx, "ns=2;s=gas", "ns=2;s=set_extractor", []any{"true"}); e == nil {
		t.Fatal("invalid method argument accepted")
	}
	if e = verifyServerCertificate(der, clientCert, endpoint); e == nil {
		t.Fatal("untrusted server certificate accepted")
	}
	if e = verifyServerCertificate(der, caFile, "opc.tcp://unregistered.invalid:4840"); e == nil {
		t.Fatal("certificate name mismatch accepted")
	}
}

type capturePublisher struct {
	ctx context.Context
	ch  chan *dt.DeviceMessage
}

func (p *capturePublisher) Publish(v *dt.DeviceMessage) error {
	select {
	case p.ch <- v:
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}
func TestNativeSubscriptionRecoversAfterProtocolServerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	port := unusedPort(t)
	start := func(value float64) *exec.Cmd {
		t.Helper()
		exe, e := os.Executable()
		if e != nil {
			t.Fatal(e)
		}
		process := exec.CommandContext(ctx, exe, "-test.run=^TestNativeServerProcessFixture$")
		process.Env = append(os.Environ(), "SF_UA_TEST_PORT="+strconv.Itoa(port), "SF_UA_TEST_VALUE="+strconv.FormatFloat(value, 'g', -1, 64))
		if e = process.Start(); e != nil {
			t.Fatal(e)
		}
		waitListening(t, port, ctx)
		return process
	}
	srv := start(20)
	publisher := &capturePublisher{ctx: ctx, ch: make(chan *dt.DeviceMessage, 256)}
	cfg := config.ConnectorConfig{ConnectorID: "ua-recovery", Protocol: "opcua", Connection: config.ConnectionConfig{URL: fmt.Sprintf("opc.tcp://127.0.0.1:%d", port), TimeoutMillis: 500}, Devices: []config.DeviceConfig{{DeviceID: "sensor", Datapoints: []config.DatapointConfig{{Key: "temperature", NodeID: "ns=1;s=temperature", DataType: "double"}}}}}
	manager, e := connector.NewManager([]config.ConnectorConfig{cfg}, publisher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- manager.Start(ctx) }()
	wait := func(want float64) {
		t.Helper()
		for {
			select {
			case msg := <-publisher.ch:
				for _, p := range msg.GetTelemetry().GetDatapoints() {
					if p.Quality == dt.DataQuality_GOOD && p.GetValue().GetDoubleValue() == want {
						return
					}
				}
			case <-ctx.Done():
				t.Fatal("subscription did not recover", want)
			case err := <-done:
				t.Fatal("connector manager stopped", err)
			}
		}
	}
	wait(20)
	if e = srv.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = srv.Wait()
	srv = start(42)
	defer func() { srv.Process.Kill(); srv.Wait() }()
	wait(42)
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("connector manager did not stop")
	}
}

func waitListening(t *testing.T, port int, ctx context.Context) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if e == nil {
			c.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatal("OPC-UA fixture did not start")
}
func TestNativeServerProcessFixture(t *testing.T) {
	raw := os.Getenv("SF_UA_TEST_PORT")
	if raw == "" {
		return
	}
	port, e := strconv.Atoi(raw)
	if e != nil {
		t.Fatal(e)
	}
	value, e := strconv.ParseFloat(os.Getenv("SF_UA_TEST_VALUE"), 64)
	if e != nil {
		t.Fatal(e)
	}
	s := server.New(server.EndPoint("127.0.0.1", port), server.EnableSecurity("None", ua.MessageSecurityModeNone), server.EnableAuthMode(ua.UserTokenTypeAnonymous), server.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	ns := server.NewMapNamespace(s, "urn:smartfactory:recovery")
	ns.Data["temperature"] = value
	if e = s.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	select {}
}

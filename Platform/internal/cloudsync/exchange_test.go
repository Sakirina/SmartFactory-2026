package cloudsync

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type fixture struct {
	cloud, edge                                  *store.Store
	server                                       *Server
	client                                       *Client
	web                                          *httptest.Server
	ca, serverCert, serverKey, edgeCert, edgeKey string
	registration                                 Registration
	entity                                       model.Entity
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Simulation CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, e := x509.CreateCertificate(rand.Reader, root, root, &caKey.PublicKey, caKey)
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{ca: filepath.Join(dir, "ca.pem")}
	if e = os.WriteFile(f.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); e != nil {
		t.Fatal(e)
	}
	makeCert := func(name, uri string, client bool) (string, string, *x509.Certificate) {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		if client {
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}
		if uri != "" {
			u, _ := url.Parse(uri)
			template.URIs = []*url.URL{u}
		}
		der, e := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, caKey)
		if e != nil {
			t.Fatal(e)
		}
		rawKey, _ := x509.MarshalPKCS8PrivateKey(key)
		certPath, keyPath := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key")
		if e = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey}), 0600); e != nil {
			t.Fatal(e)
		}
		certificate, _ := x509.ParseCertificate(der)
		return certPath, keyPath, certificate
	}
	f.serverCert, f.serverKey, _ = makeCert("cloud", "", false)
	var edgeCertificate *x509.Certificate
	f.edgeCert, f.edgeKey, edgeCertificate = makeCert("edge-a", "spiffe://smartfactory/edge/edge-a", true)
	f.cloud, e = store.Open(ctx, ":memory:", "cloud-1", bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	f.edge, e = store.Open(ctx, ":memory:", "edge-a", bytes.Repeat([]byte{2}, 32))
	if e != nil {
		t.Fatal(e)
	}
	cloudAuth := &identity.Manager{Store: f.cloud, Master: bytes.Repeat([]byte{1}, 32)}
	edgeAuth := &identity.Manager{Store: f.edge, Master: bytes.Repeat([]byte{2}, 32), Edge: true}
	f.edge.ForwardObservations = true
	public, e := edgeAuth.EncryptionPublicKey()
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(edgeCertificate.Raw)
	f.registration = Registration{CertificateSHA256: hex.EncodeToString(hash[:]), AuditPublicKey: base64.StdEncoding.EncodeToString(f.edge.SignKey.Public().(ed25519.PublicKey)), EncryptionPublicKey: public}
	raw, _ := json.Marshal(f.registration)
	f.entity = model.Entity{ID: "edge-a", Kind: "edge", Status: "active", Version: 1, Config: raw}
	if _, e = f.cloud.Put(ctx, "entity", f.entity.ID, 0, f.entity); e != nil {
		t.Fatal(e)
	}
	for _, entity := range []model.Entity{{ID: "factory", Kind: "asset", Status: "active", Version: 1}} {
		if _, e = f.cloud.Put(ctx, "entity", entity.ID, 0, entity); e != nil {
			t.Fatal(e)
		}
	}
	_, e = cloudAuth.CreateUser(ctx, model.Actor{UserID: "admin", Source: "installation"}, model.User{ID: "engineer", Name: "Engineer", Login: "engineer", Roles: []string{"engineer"}, Resources: []string{"factory"}, Active: true}, "simulated-password", "", 0)
	if e != nil {
		t.Fatal(e)
	}
	device := model.Entity{ID: "counter", Kind: "device", Status: "approved", EdgeID: "edge-a", ParentID: "factory", Version: 1}
	if _, e = f.edge.Put(ctx, "entity", device.ID, 0, device); e != nil {
		t.Fatal(e)
	}
	if e = f.edge.Audit(ctx, model.Actor{UserID: "engineer", Source: "edge-a"}, "device.approve", "counter", "", device); e != nil {
		t.Fatal(e)
	}
	f.server = &Server{Store: f.cloud, Identity: cloudAuth}
	f.web = httptest.NewUnstartedServer(f.server.Handler())
	f.web.TLS, e = f.server.TLSConfig(f.ca, f.serverCert, f.serverKey)
	if e != nil {
		t.Fatal(e)
	}
	f.web.StartTLS()
	httpClient, e := NewHTTPClient(f.ca, f.edgeCert, f.edgeKey)
	if e != nil {
		t.Fatal(e)
	}
	f.client = &Client{Store: f.edge, Identity: edgeAuth, HTTP: httpClient, URL: f.web.URL, CloudSigningKey: f.cloud.SignKey.Public().(ed25519.PublicKey)}
	t.Cleanup(func() { f.web.Close(); f.edge.Close(); f.cloud.Close() })
	return f
}

type loseResponse struct {
	http.RoundTripper
	once bool
}

func (r *loseResponse) RoundTrip(q *http.Request) (*http.Response, error) {
	response, e := r.RoundTripper.RoundTrip(q)
	if e != nil {
		return nil, e
	}
	if !r.once && response.StatusCode == 200 {
		r.once = true
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return nil, errors.New("injected response acknowledgement loss")
	}
	return response, nil
}

func TestMutualTLSSynchronizationReplayAndOfflinePermissions(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		id := identity.ID()
		batch := store.IngestBatch{MessageID: id, SourceID: "edge-a", Critical: true, Points: []model.Observation{{DeviceID: "counter", Key: "pulse", Value: json.Number("9007199254740993"), ObservedMS: time.Now().UnixMilli(), Quality: "GOOD", TimeSource: "device"}}, Event: map[string]any{"pulse": 1}}
		if _, e := f.edge.Ingest(ctx, batch); e != nil {
			t.Fatal(e)
		}
	}
	f.client.HTTP.Transport = &loseResponse{RoundTripper: f.client.HTTP.Transport}
	if e := f.client.Exchange(ctx); e == nil {
		t.Fatal("missing injected response loss")
	}
	pending, _ := f.edge.Deliveries(ctx, "cloud_observation", 1000)
	if len(pending) != 100 {
		t.Fatal("ack loss removed pending records")
	}
	if e := f.client.Exchange(ctx); e != nil {
		t.Fatal(e)
	}
	pending, _ = f.edge.Deliveries(ctx, "cloud_observation", 1000)
	if len(pending) != 0 {
		t.Fatal("committed records not retired")
	}
	events, _ := f.cloud.List(ctx, "event")
	if len(events) != 100 {
		t.Fatal("duplicate business events", len(events))
	}
	result, e := f.cloud.Query(ctx, store.Query{DeviceIDs: []string{"counter"}, Keys: []string{"pulse"}, FromMS: time.Now().Add(-time.Hour).UnixMilli(), ToMS: time.Now().Add(time.Hour).UnixMilli(), Limit: 1000})
	if e != nil || len(result.Points) != 100 {
		t.Fatal(e, len(result.Points))
	}
	if result.Points[0].Value != json.Number("9007199254740993") {
		t.Fatal("integer precision lost")
	}
	issues, e := f.cloud.VerifyAudit(ctx)
	if e != nil || len(issues) > 0 {
		t.Fatal(e, issues)
	}
	if _, _, e = f.client.Identity.Login(ctx, "engineer", "simulated-password", "", true, "local"); e != nil {
		t.Fatal("synced offline login", e)
	}
	for i := 0; i < 3; i++ {
		if e = f.client.Exchange(ctx); e != nil {
			t.Fatal(e)
		}
	}
	versions, e := f.edge.Versions(ctx, "user", "engineer")
	if e != nil || len(versions) != 1 {
		t.Fatal("unchanged permission snapshot was reapplied", e, len(versions))
	}
	f.entity.Status = "disabled"
	f.entity.Version = 2
	if _, e = f.cloud.Put(ctx, "entity", "edge-a", 1, f.entity); e != nil {
		t.Fatal(e)
	}
	if e = f.client.Exchange(ctx); e == nil {
		t.Fatal("disabled node reused existing connection")
	}
	// Its already signed package remains usable during the agreed offline TTL.
	if _, _, e = f.client.Identity.Login(ctx, "engineer", "simulated-password", "", true, "local"); e != nil {
		t.Fatal(e)
	}
}

func TestAdmissionRequiresCertificateAndAvailableRegistry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	roots, e := RootCAs(f.ca)
	if e != nil {
		t.Fatal(e)
	}
	anonymous := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}}
	if response, e := anonymous.Post(f.web.URL+"/internal/sync/v1/exchange", "application/json", bytes.NewReader([]byte(`{}`))); e == nil {
		response.Body.Close()
		t.Fatal("anonymous TLS connection accepted")
	}
	if e = f.client.Exchange(ctx); e != nil {
		t.Fatal(e)
	}
	f.cloud.Close()
	if e = f.client.Exchange(ctx); e == nil {
		t.Fatal("database failure admitted request on existing connection")
	}
	fresh, e := NewHTTPClient(f.ca, f.edgeCert, f.edgeKey)
	if e != nil {
		t.Fatal(e)
	}
	f.client.HTTP = fresh
	if e = f.client.Exchange(ctx); e == nil {
		t.Fatal("database failure admitted new TLS connection")
	}
}

func TestChangedContentAndForeignOwnerAreNotAcknowledged(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if e := f.client.Exchange(ctx); e != nil {
		t.Fatal(e)
	}
	batch := store.IngestBatch{MessageID: "fixed", SourceID: "edge-a", Critical: true, Points: []model.Observation{{DeviceID: "counter", Key: "pulse", Value: 1, ObservedMS: time.Now().UnixMilli(), Quality: "GOOD"}}}
	raw, _ := json.Marshal(batch)
	delivery := store.Delivery{ID: "upstream:fixed", Kind: "cloud_observation", Payload: raw}
	if e := f.server.receive(ctx, "edge-a", delivery); e != nil {
		t.Fatal(e)
	}
	batch.Points[0].Value = 2
	delivery.Payload, _ = json.Marshal(batch)
	if e := f.server.receive(ctx, "edge-a", delivery); !errors.Is(e, store.ErrConflict) {
		t.Fatal("changed payload accepted", e)
	}
	if e := f.server.receive(ctx, "edge-b", delivery); e == nil {
		t.Fatal("foreign edge accepted")
	}
	before, _ := f.cloud.ExportAudit(ctx, 0, 100)
	if _, e := f.cloud.ImportAudit(ctx, "edge-a", f.edge.SignKey.Public().(ed25519.PublicKey), before); e == nil {
		t.Fatal("foreign source audit accepted")
	}
}

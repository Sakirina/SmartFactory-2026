package cloudsync

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Registration struct {
	CertificateSHA256   string `json:"certificate_sha256"`
	AuditPublicKey      string `json:"audit_public_key"`
	EncryptionPublicKey string `json:"encryption_public_key"`
}

func Identity(cert *x509.Certificate) (string, error) {
	for _, uri := range cert.URIs {
		if uri.Scheme == "spiffe" && uri.Host == "smartfactory" && strings.HasPrefix(uri.Path, "/edge/") {
			id := strings.TrimPrefix(uri.Path, "/edge/")
			if id != "" && !strings.Contains(id, "/") {
				return id, nil
			}
		}
	}
	return "", errors.New("registered edge URI SAN is required")
}
func (s *Server) Admit(ctx context.Context, cert *x509.Certificate) (string, Registration, error) {
	node, e := Identity(cert)
	if e != nil {
		return "", Registration{}, e
	}
	d, e := s.Store.Get(ctx, "entity", node)
	if e != nil {
		return "", Registration{}, e
	}
	entity, e := store.Decode[model.Entity](d)
	if e != nil {
		return "", Registration{}, e
	}
	if entity.Kind != "edge" || entity.Status != "active" {
		return "", Registration{}, errors.New("edge is not admitted")
	}
	var registration Registration
	if e = store.DecodeJSON(entity.Config, &registration); e != nil {
		return "", registration, e
	}
	hash := sha256.Sum256(cert.Raw)
	if registration.CertificateSHA256 != hex.EncodeToString(hash[:]) {
		return "", registration, errors.New("certificate fingerprint is not registered")
	}
	public, e := base64.StdEncoding.DecodeString(registration.AuditPublicKey)
	if e != nil || len(public) != ed25519.PublicKeySize || registration.EncryptionPublicKey == "" {
		return "", registration, errors.New("node signing and encryption keys must be registered")
	}
	return node, registration, nil
}
func RootCAs(path string) (*x509.CertPool, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, errors.New("CA file contains no certificate")
	}
	return pool, nil
}
func (s *Server) TLSConfig(ca, certificate, key string) (*tls.Config, error) {
	roots, e := RootCAs(ca)
	if e != nil {
		return nil, e
	}
	cert, e := tls.LoadX509KeyPair(certificate, key)
	if e != nil {
		return nil, e
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, SessionTicketsDisabled: true, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("client certificate required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _, e := s.Admit(ctx, state.PeerCertificates[0])
		return e
	}}, nil
}
func NewHTTPClient(ca, certificate, key string) (*http.Client, error) {
	roots, e := RootCAs(ca)
	if e != nil {
		return nil, e
	}
	cert, e := tls.LoadX509KeyPair(certificate, key)
	if e != nil {
		return nil, e
	}
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("sync redirects are disabled") }, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}}, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}}, nil
}

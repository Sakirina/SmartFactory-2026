// sf-pki creates a local CA and node certificates for reproducible deployments.
package main

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func write(path string, data []byte) error {
	file, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = file.Write(data)
	closeErr := file.Close()
	if e != nil {
		return e
	}
	return closeErr
}
func certificate(path string) (*x509.Certificate, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("invalid certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}
func run() error {
	mode := flag.String("mode", "issue", "init, issue or public-keys")
	dir := flag.String("dir", ".local/pki", "certificate output directory")
	node := flag.String("node", "edge-a", "registered node ID")
	client := flag.Bool("client", false, "issue client certificate with registered edge URI SAN")
	names := flag.String("hosts", "localhost,127.0.0.1", "server DNS names or IP addresses")
	master := flag.String("master-key", "", "node master key used only to derive public key information")
	flag.Parse()
	if strings.ContainsAny(*node, "/\\") || *node == "" {
		return errors.New("node must be a simple identifier")
	}
	if e := os.MkdirAll(*dir, 0700); e != nil {
		return e
	}
	if *mode == "public-keys" {
		raw, e := os.ReadFile(*master)
		if e != nil {
			return e
		}
		key, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if e != nil || len(key) != 32 {
			return errors.New("invalid master key")
		}
		signSeed := sha256.Sum256(append(append([]byte{}, key...), []byte("audit/"+*node)...))
		sign := ed25519.NewKeyFromSeed(signSeed[:]).Public().(ed25519.PublicKey)
		wrappingSeed := sha256.Sum256(append(append([]byte{}, key...), []byte("smartfactory/permission-envelope/v1")...))
		wrapping, e := ecdh.X25519().NewPrivateKey(wrappingSeed[:])
		if e != nil {
			return e
		}
		cert, e := certificate(filepath.Join(*dir, *node+".pem"))
		if e != nil {
			return e
		}
		hash := sha256.Sum256(cert.Raw)
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"certificate_sha256": hex.EncodeToString(hash[:]), "audit_public_key": base64.StdEncoding.EncodeToString(sign), "encryption_public_key": base64.StdEncoding.EncodeToString(wrapping.PublicKey().Bytes())})
	}
	caPath, caKeyPath := filepath.Join(*dir, "ca.pem"), filepath.Join(*dir, "ca.key")
	if *mode == "init" {
		if _, e := os.Stat(caPath); e == nil {
			_, e = certificate(caPath)
			return e
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return e
		}
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			return e
		}
		ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "SmartFactory installation CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		der, e := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
		if e != nil {
			return e
		}
		rawKey, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			return e
		}
		if e = write(caKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey})); e != nil {
			return e
		}
		return write(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	if *mode != "issue" {
		return errors.New("unknown mode")
	}
	certPath, keyPath := filepath.Join(*dir, *node+".pem"), filepath.Join(*dir, *node+".key")
	if _, e := os.Stat(certPath); e == nil {
		_, e = certificate(certPath)
		return e
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	ca, e := certificate(caPath)
	if e != nil {
		return e
	}
	raw, e := os.ReadFile(caKeyPath)
	if e != nil {
		return e
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return errors.New("invalid CA private key")
	}
	caKey, e := x509.ParsePKCS8PrivateKey(block.Bytes)
	if e != nil {
		return e
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return e
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: *node}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, name := range strings.Split(*names, ",") {
		if ip := net.ParseIP(name); ip != nil {
			cert.IPAddresses = append(cert.IPAddresses, ip)
		} else {
			cert.DNSNames = append(cert.DNSNames, name)
		}
	}
	if *client {
		uri, e := url.Parse("spiffe://smartfactory/edge/" + *node)
		if e != nil {
			return e
		}
		cert.URIs = []*url.URL{uri}
		cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
	}
	der, e := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	if e != nil {
		return e
	}
	rawKey, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return e
	}
	if e = write(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey})); e != nil {
		return e
	}
	return write(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

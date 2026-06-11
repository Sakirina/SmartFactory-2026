package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	reflectpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
)

// TestGRPCReflectionOverTLSInDevelopment 验证:development 环境显式开启反射后,
// 反射服务在 TLS 加密的 gRPC 端口上可用,且能列出 DataTransferService。
func TestGRPCReflectionOverTLSInDevelopment(t *testing.T) {
	certFile, keyFile, caPool := writeSelfSignedCert(t)
	managementAddr := freeAddr(t)
	grpcAddr := freeAddr(t)
	configPath := filepath.Join(t.TempDir(), "datatransfer.yaml")
	data := []byte(fmt.Sprintf(`
environment: development
run_mode: embedded
log:
  level: error
management:
  addr: %q
grpc:
  enabled: true
  addr: %q
  reflection: true
  tls:
    enabled: true
    cert_file: %q
    key_file: %q
mqtt:
  enabled: false
connectors: []
`, managementAddr, grpcAddr, certFile, keyFile))
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (App{ConfigPath: configPath}).Run(ctx)
	}()
	waitForHTTPStatus(t, "http://"+managementAddr+"/healthz", http.StatusOK)

	tlsCreds := credentials.NewTLS(&tls.Config{RootCAs: caPool, MinVersion: tls.VersionTLS12})
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(tlsCreds))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	services := listServicesViaReflection(t, conn)
	if !containsService(services, "datatransfer.v1.DataTransferService") {
		t.Fatalf("reflection services = %v, want datatransfer.v1.DataTransferService", services)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("App.Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for App.Run shutdown")
	}
}

// TestGRPCReflectionDisabledByDefault 验证:默认(production、未开启反射)
// 反射端点不注册,调用反射接口收到错误。
func TestGRPCReflectionDisabledByDefault(t *testing.T) {
	managementAddr := freeAddr(t)
	grpcAddr := freeAddr(t)
	configPath := filepath.Join(t.TempDir(), "datatransfer.yaml")
	data := []byte(fmt.Sprintf(`
run_mode: embedded
log:
  level: error
management:
  addr: %q
grpc:
  enabled: true
  addr: %q
mqtt:
  enabled: false
connectors: []
`, managementAddr, grpcAddr))
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (App{ConfigPath: configPath}).Run(ctx)
	}()
	waitForHTTPStatus(t, "http://"+managementAddr+"/healthz", http.StatusOK)

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	callCtx, callCancel := context.WithTimeout(context.Background(), time.Second)
	defer callCancel()
	stream, err := reflectpb.NewServerReflectionClient(conn).ServerReflectionInfo(callCtx)
	if err == nil {
		sendErr := stream.Send(&reflectpb.ServerReflectionRequest{
			MessageRequest: &reflectpb.ServerReflectionRequest_ListServices{ListServices: ""},
		})
		if sendErr == nil {
			if _, recvErr := stream.Recv(); recvErr == nil {
				t.Fatal("reflection responded on a default (production) server; it must be unregistered")
			}
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("App.Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for App.Run shutdown")
	}
}

func listServicesViaReflection(t *testing.T, conn *grpc.ClientConn) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := reflectpb.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
	if err != nil {
		t.Fatalf("ServerReflectionInfo: %v", err)
	}
	if err := stream.Send(&reflectpb.ServerReflectionRequest{
		MessageRequest: &reflectpb.ServerReflectionRequest_ListServices{ListServices: ""},
	}); err != nil {
		t.Fatalf("reflection send: %v", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("reflection recv: %v", err)
	}
	list := resp.GetListServicesResponse()
	if list == nil {
		t.Fatalf("reflection response has no service list: %v", resp)
	}
	services := make([]string, 0, len(list.GetService()))
	for _, service := range list.GetService() {
		services = append(services, service.GetName())
	}
	return services
}

func containsService(services []string, name string) bool {
	for _, service := range services {
		if service == name {
			return true
		}
	}
	return false
}

// writeSelfSignedCert 生成测试用自签服务端证书(SAN=127.0.0.1/localhost),
// 返回证书与私钥路径以及客户端校验用的 CA 池。
func writeSelfSignedCert(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "datatransfer-dev"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("append self-signed cert to pool")
	}
	return certFile, keyFile, pool
}

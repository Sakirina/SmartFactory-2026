// Package security 提供 TLS 初始化入口(DQ-008 TLSProvider 工程预留点)。
// 后续国密(SM2/SM3/SM4)适配在此边界新增 Provider 实现,调用方不感知。
package security

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"

	"competition2026/product/datatransfer/internal/config"
)

// ServerTLSConfig 构造 gRPC/HTTP 服务端 TLS:cert/key 为服务端证书,
// ca_file 配置后启用 mTLS(校验客户端证书)。与客户端侧 TLSConfig 区分:
// 服务端用 ClientCAs+ClientAuth,客户端用 RootCAs。
func ServerTLSConfig(cfg config.TLSConfig) (*tls.Config, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server key pair: %w", err)
	}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if cfg.CAFile != "" {
		ca, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("ca_file %q contains no valid PEM certificates", cfg.CAFile)
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return tlsCfg, nil
}

// TLSConfig 构造客户端侧 TLS(MQTT Broker、南向设备连接等)。
func TLSConfig(cfg config.TLSConfig) (*tls.Config, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	tlsCfg := &tls.Config{
		// 工业边缘场景禁止 TLS 1.0/1.1;Go 默认值随版本变化,显式钉住下限。
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
	if cfg.InsecureSkipVerify {
		slog.Warn("TLS certificate verification is DISABLED (insecure_skip_verify); only acceptable in test environments")
	}
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	if cfg.CAFile != "" {
		ca, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			// 空池会导致所有服务端证书校验失败且错误信息难以定位,这里直接拒绝启动。
			return nil, fmt.Errorf("ca_file %q contains no valid PEM certificates", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	return tlsCfg, nil
}

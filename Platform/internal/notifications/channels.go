package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Configuration interface {
	Value(context.Context, string) (any, error)
}
type Channels struct{ Config Configuration }
type Channel struct {
	Enabled        bool   `json:"enabled"`
	Endpoint       string `json:"endpoint"`
	Token          string `json:"token"`
	TimeoutMS      int64  `json:"timeout_ms"`
	SMTPAddress    string `json:"smtp_address"`
	SMTPServerName string `json:"smtp_server_name"`
	SMTPTLSMode    string `json:"smtp_tls_mode"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	From           string `json:"from"`
}

func (c *Channels) Send(ctx context.Context, d Delivery, u model.User) (Result, error) {
	if c.Config == nil {
		return Result{}, errors.New("notification channel configuration unavailable")
	}
	value, err := c.Config.Value(ctx, "notification."+d.Channel)
	if err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return Result{}, err
	}
	var cfg Channel
	if err = store.DecodeJSON(raw, &cfg); err != nil {
		return Result{}, err
	}
	if !cfg.Enabled {
		return Result{}, errors.New("notification channel is disabled")
	}
	duration := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if duration < time.Second || duration > time.Minute {
		duration = 10 * time.Second
	}
	call, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	switch d.Channel {
	case "email":
		return sendSMTP(call, cfg, d, u)
	case "sms":
		return sendWebhook(call, cfg, d, u)
	default:
		return Result{}, errors.New("unsupported notification channel")
	}
}
func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}
func sendWebhook(ctx context.Context, cfg Channel, d Delivery, u model.User) (Result, error) {
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || (endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && loopback(endpoint.Hostname()))) {
		return Result{}, errors.New("SMS endpoint requires HTTPS or a loopback test receiver")
	}
	if u.Phone == "" {
		return Result{}, errors.New("recipient has no phone number")
	}
	raw, _ := json.Marshal(map[string]any{"notification_id": d.ID, "phone": u.Phone, "message": Body(d), "alarm": d.Alarm})
	request, err := http.NewRequestWithContext(ctx, "POST", cfg.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", d.ID)
	// The durable worker owns retries, including the lost-response decision.
	request.GetBody = nil
	if cfg.Token != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	var wrote atomic.Bool
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) }}))
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("notification redirects refused") }}
	response, err := client.Do(request)
	if err != nil {
		return Result{MayHaveSent: wrote.Load()}, errors.New("SMS delivery acknowledgement unavailable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{MayHaveSent: response.StatusCode >= 500}, fmt.Errorf("SMS gateway rejected delivery with HTTP %d", response.StatusCode)
	}
	return Result{Confirmed: true}, nil
}
func sendSMTP(ctx context.Context, cfg Channel, d Delivery, u model.User) (Result, error) {
	host, _, err := net.SplitHostPort(cfg.SMTPAddress)
	if err != nil {
		return Result{}, errors.New("SMTP address must include host and port")
	}
	if !noNewlines(cfg.From) || !noNewlines(u.Email) {
		return Result{}, errors.New("invalid email address")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return Result{}, errors.New("sender email required")
	}
	to, err := mail.ParseAddress(u.Email)
	if err != nil {
		return Result{}, errors.New("recipient email required")
	}
	name := cfg.SMTPServerName
	if name == "" {
		name = host
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: name}
	var connection net.Conn
	dialer := &net.Dialer{}
	if cfg.SMTPTLSMode == "implicit" {
		connection, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", cfg.SMTPAddress)
	} else {
		if cfg.SMTPTLSMode != "starttls" && !(cfg.SMTPTLSMode == "none" && loopback(host)) {
			return Result{}, errors.New("SMTP TLS is required")
		}
		connection, err = dialer.DialContext(ctx, "tcp", cfg.SMTPAddress)
	}
	if err != nil {
		return Result{}, errors.New("SMTP connection failed")
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	client, err := smtp.NewClient(connection, name)
	if err != nil {
		return Result{}, errors.New("SMTP greeting failed")
	}
	defer client.Close()
	if cfg.SMTPTLSMode == "starttls" {
		if err = client.StartTLS(tlsConfig); err != nil {
			return Result{}, errors.New("SMTP TLS negotiation failed")
		}
	}
	if cfg.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, name)); err != nil {
			return Result{}, errors.New("SMTP authentication failed")
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return Result{}, errors.New("SMTP sender rejected")
	}
	if err = client.Rcpt(to.Address); err != nil {
		return Result{}, errors.New("SMTP recipient rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return Result{}, errors.New("SMTP message rejected")
	}
	body := "From: " + from.String() + "\r\nTo: " + to.String() + "\r\nSubject: SmartFactory alarm\r\nMessage-ID: <" + store.Hash(d.ID) + "@smartfactory.local>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + strings.ReplaceAll(Body(d), "\n", "\r\n")
	if _, err = io.WriteString(writer, body); err != nil {
		return Result{MayHaveSent: true}, errors.New("SMTP acknowledgement unavailable")
	}
	if err = writer.Close(); err != nil {
		return Result{MayHaveSent: true}, errors.New("SMTP acknowledgement unavailable")
	}
	_ = client.Quit()
	return Result{Confirmed: true}, nil
}

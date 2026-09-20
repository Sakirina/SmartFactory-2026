package opcua

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	dtv1 "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/datatransfer/internal/config"
	gopcua "github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// Observation retains the server's quality and timestamp even when its value is unavailable.
type Observation struct {
	Value      any
	Quality    dtv1.DataQuality
	Reason     string
	Timestamp  time.Time
	TimeSource string
}

func observation(value *ua.DataValue) Observation {
	o := Observation{Quality: dtv1.DataQuality_BAD, Reason: "missing OPC-UA DataValue", Timestamp: time.Now(), TimeSource: "collector"}
	if value == nil {
		return o
	}
	if value.Value != nil {
		o.Value = value.Value.Value()
	}
	switch uint32(value.Status) & 0xc0000000 {
	case 0:
		o.Quality = dtv1.DataQuality_GOOD
		o.Reason = ""
	case 0x40000000:
		o.Quality = dtv1.DataQuality_UNCERTAIN
		o.Reason = value.Status.Error()
	default:
		o.Reason = value.Status.Error()
	}
	if value.Value == nil && o.Quality == dtv1.DataQuality_GOOD {
		o.Quality, o.Reason = dtv1.DataQuality_BAD, "OPC-UA value is absent"
	}
	if !value.SourceTimestamp.IsZero() {
		o.Timestamp, o.TimeSource = value.SourceTimestamp, "device"
	} else if !value.ServerTimestamp.IsZero() {
		o.Timestamp, o.TimeSource = value.ServerTimestamp, "protocol_server"
	}
	return o
}

func makeDatapoint(value any, dp config.DatapointConfig) *dtv1.Datapoint {
	p := &dtv1.Datapoint{Key: dp.Key, Timestamp: time.Now().UnixMilli(), Quality: qualityFromString(dp.Quality), Unit: dp.Unit, TimeSource: "collector"}
	if sample, ok := value.(Observation); ok {
		value = sample.Value
		p.Timestamp, p.TimeSource = sample.Timestamp.UnixMilli(), sample.TimeSource
		p.Quality, p.QualityReason = sample.Quality, sample.Reason
	}
	if value != nil {
		p.Value = dataValue(value, dp)
		if p.Value == nil {
			p.Quality = dtv1.DataQuality_BAD
			p.QualityReason = "value does not match the configured scalar type"
		}
	}
	return p
}

type nativeClient struct {
	cfg    config.ConnectorConfig
	client *gopcua.Client
	rateMu sync.Mutex
	rate   chan int64
}

func nativeClientFactory(cfg config.ConnectorConfig) (Client, error) {
	conn := &cfg.Connection
	if conn.TLS.Enabled {
		if conn.SecurityMode == "" {
			conn.SecurityMode = "SignAndEncrypt"
		}
		if conn.SecurityPolicy == "" {
			conn.SecurityPolicy = "Basic256Sha256"
		}
		if conn.CertFile == "" {
			conn.CertFile = conn.TLS.CertFile
		}
		if conn.KeyFile == "" {
			conn.KeyFile = conn.TLS.KeyFile
		}
		if conn.CAFile == "" {
			conn.CAFile = conn.TLS.CAFile
		}
	}
	if conn.SecurityMode == "" {
		conn.SecurityMode = "None"
	}
	if conn.SecurityPolicy == "" {
		conn.SecurityPolicy = "None"
	}
	mode := ua.MessageSecurityModeFromString(conn.SecurityMode)
	if mode == ua.MessageSecurityModeInvalid {
		return nil, fmt.Errorf("invalid OPC-UA security_mode %q", conn.SecurityMode)
	}
	if mode != ua.MessageSecurityModeNone {
		if conn.CertFile == "" || conn.KeyFile == "" || conn.CAFile == "" {
			return nil, fmt.Errorf("OPC-UA signed connections require cert_file, key_file and ca_file")
		}
		if strings.HasSuffix(conn.SecurityPolicy, "None") {
			return nil, fmt.Errorf("OPC-UA signed connections require an encryption security_policy")
		}
	} else if conn.TLS.Enabled || !strings.HasSuffix(conn.SecurityPolicy, "None") {
		return nil, fmt.Errorf("OPC-UA security_mode None conflicts with certificate security settings")
	}
	if conn.Username != "" && mode != ua.MessageSecurityModeSignAndEncrypt {
		return nil, fmt.Errorf("OPC-UA password authentication requires SignAndEncrypt")
	}
	return &nativeClient{cfg: cfg}, nil
}

func (c *nativeClient) Connect(ctx context.Context) error {
	cfg := c.cfg.Connection
	endpoints, err := gopcua.GetEndpoints(ctx, cfg.URL)
	if err != nil {
		return err
	}
	mode := ua.MessageSecurityModeFromString(cfg.SecurityMode)
	ep, err := gopcua.SelectEndpoint(endpoints, cfg.SecurityPolicy, mode)
	if err != nil {
		return err
	}
	if mode != ua.MessageSecurityModeNone {
		if err := verifyServerCertificate(ep.ServerCertificate, cfg.CAFile, cfg.URL); err != nil {
			return err
		}
	}
	ep.EndpointURL = cfg.URL
	authType := ua.UserTokenTypeAnonymous
	opts := []gopcua.Option{gopcua.AuthAnonymous(), gopcua.AutoReconnect(true)}
	if cfg.Username != "" {
		authType = ua.UserTokenTypeUserName
		opts = append(opts, gopcua.AuthUsername(cfg.Username, cfg.Password))
	}
	opts = append(opts, gopcua.SecurityFromEndpoint(ep, authType))
	if mode != ua.MessageSecurityModeNone {
		certificate, key, err := loadIdentity(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return err
		}
		opts = append(opts, gopcua.Certificate(certificate), gopcua.PrivateKey(key))
	}
	if cfg.TimeoutMillis > 0 {
		opts = append(opts, gopcua.RequestTimeout(time.Duration(cfg.TimeoutMillis)*time.Millisecond))
	}
	client, err := gopcua.NewClient(cfg.URL, opts...)
	if err != nil {
		return err
	}
	c.client = client
	return client.Connect(ctx)
}

// Accept standard PEM and DER encodings independent of file name extensions.
func loadIdentity(certificateFile, keyFile string) ([]byte, *rsa.PrivateKey, error) {
	certificate, e := os.ReadFile(certificateFile)
	if e != nil {
		return nil, nil, e
	}
	if block, _ := pem.Decode(certificate); block != nil {
		certificate = block.Bytes
	}
	parsed, e := x509.ParseCertificate(certificate)
	if e != nil {
		return nil, nil, fmt.Errorf("OPC-UA client certificate: %w", e)
	}
	raw, e := os.ReadFile(keyFile)
	if e != nil {
		return nil, nil, e
	}
	if block, _ := pem.Decode(raw); block != nil {
		raw = block.Bytes
	}
	key, e := x509.ParsePKCS1PrivateKey(raw)
	if e != nil {
		value, parseErr := x509.ParsePKCS8PrivateKey(raw)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("OPC-UA private key must use PKCS#1 or PKCS#8")
		}
		var ok bool
		key, ok = value.(*rsa.PrivateKey)
		if !ok {
			return nil, nil, fmt.Errorf("OPC-UA security policy requires an RSA private key")
		}
	}
	public, ok := parsed.PublicKey.(*rsa.PublicKey)
	if !ok || public.E != key.PublicKey.E || public.N.Cmp(key.PublicKey.N) != 0 {
		return nil, nil, fmt.Errorf("OPC-UA certificate and private key do not match")
	}
	if e = key.Validate(); e != nil {
		return nil, nil, e
	}
	return certificate, key, nil
}

func verifyServerCertificate(der []byte, caFile, endpoint string) error {
	chain, err := x509.ParseCertificates(der)
	if err != nil || len(chain) == 0 {
		return fmt.Errorf("invalid OPC-UA server certificate: %w", err)
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return fmt.Errorf("OPC-UA CA file contains no certificates")
	}
	intermediates := x509.NewCertPool()
	for _, cert := range chain[1:] {
		intermediates.AddCert(cert)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	_, err = chain[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: u.Hostname(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	if err != nil {
		return fmt.Errorf("OPC-UA server certificate verification: %w", err)
	}
	return nil
}

func (c *nativeClient) Close(ctx context.Context) error {
	if c.client == nil {
		return nil
	}
	return c.client.Close(ctx)
}

func (c *nativeClient) Read(ctx context.Context, nodeID string) (any, error) {
	id, err := ua.ParseNodeID(nodeID)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Read(ctx, &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: id, AttributeID: ua.AttributeIDValue}}, TimestampsToReturn: ua.TimestampsToReturnBoth})
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Results) != 1 {
		return nil, fmt.Errorf("OPC-UA read returned no result")
	}
	return observation(resp.Results[0]), nil
}

func (c *nativeClient) Write(ctx context.Context, nodeID string, value any) error {
	id, err := ua.ParseNodeID(nodeID)
	if err != nil {
		return err
	}
	variant, err := ua.NewVariant(value)
	if err != nil {
		return err
	}
	resp, err := c.client.Write(ctx, &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{NodeID: id, AttributeID: ua.AttributeIDValue, Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: variant}}}})
	if err != nil {
		return err
	}
	if resp == nil || len(resp.Results) != 1 {
		return fmt.Errorf("OPC-UA write returned no result")
	}
	if uint32(resp.Results[0])&0xc0000000 != 0 {
		return fmt.Errorf("OPC-UA write %s: %w", nodeID, resp.Results[0])
	}
	return nil
}

func (c *nativeClient) Call(ctx context.Context, objectID, methodID string, args []any) ([]any, error) {
	object, err := ua.ParseNodeID(objectID)
	if err != nil {
		return nil, err
	}
	method, err := ua.ParseNodeID(methodID)
	if err != nil {
		return nil, err
	}
	variants := make([]*ua.Variant, 0, len(args))
	for _, arg := range args {
		variant, err := ua.NewVariant(arg)
		if err != nil {
			return nil, err
		}
		variants = append(variants, variant)
	}
	result, err := c.client.Call(ctx, &ua.CallMethodRequest{ObjectID: object, MethodID: method, InputArguments: variants})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("OPC-UA call returned no result")
	}
	if uint32(result.StatusCode)&0xc0000000 != 0 {
		return nil, fmt.Errorf("OPC-UA call %s: %w", methodID, result.StatusCode)
	}
	out := make([]any, 0, len(result.OutputArguments))
	for _, value := range result.OutputArguments {
		if value == nil {
			out = append(out, nil)
		} else {
			out = append(out, value.Value())
		}
	}
	return out, nil
}

func (c *nativeClient) Subscribe(ctx context.Context, nodes []string, emit func(string, any)) error {
	c.rateMu.Lock()
	if c.rate == nil {
		c.rate = make(chan int64, 1)
	}
	rate := c.rate
	c.rateMu.Unlock()
	notifications := make(chan *gopcua.PublishNotificationData, 64)
	interval := 250 * time.Millisecond
	if c.cfg.Polling.IntervalMillis > 0 {
		interval = time.Duration(c.cfg.Polling.IntervalMillis) * time.Millisecond
	}
	publishInterval := interval
	if c.cfg.Polling.PublishIntervalMillis > 0 {
		publishInterval = time.Duration(c.cfg.Polling.PublishIntervalMillis) * time.Millisecond
	}
	sub, err := c.client.Subscribe(ctx, &gopcua.SubscriptionParameters{Interval: publishInterval}, notifications)
	if err != nil {
		return err
	}
	defer func() {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = sub.Cancel(cancelCtx)
	}()
	requests := make([]*ua.MonitoredItemCreateRequest, 0, len(nodes))
	for i, node := range nodes {
		id, err := ua.ParseNodeID(node)
		if err != nil {
			return err
		}
		request := gopcua.NewMonitoredItemCreateRequestWithDefaults(id, ua.AttributeIDValue, uint32(i))
		request.RequestedParameters.SamplingInterval = float64(interval / time.Millisecond)
		request.RequestedParameters.QueueSize = 64
		requests = append(requests, request)
	}
	result, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, requests...)
	if err != nil {
		return err
	}
	if result == nil || len(result.Results) != len(nodes) {
		return fmt.Errorf("OPC-UA monitor returned an incomplete result")
	}
	for i, item := range result.Results {
		if item == nil || item.StatusCode != ua.StatusOK {
			return fmt.Errorf("OPC-UA monitor failed for %s: %v", nodes[i], item)
		}
	}
	connectionWatch := time.NewTicker(250 * time.Millisecond)
	defer connectionWatch.Stop()
	for {
		select {
		case <-connectionWatch.C:
			if c.client.State() != gopcua.Connected {
				return fmt.Errorf("OPC-UA session disconnected; recreate subscriptions")
			}
		case factor := <-rate:
			desired := interval * time.Duration(factor)
			if _, err = sub.ModifySubscription(ctx, gopcua.SubscriptionParameters{Interval: publishInterval * time.Duration(factor)}); err != nil {
				return err
			}
			items := make([]*ua.MonitoredItemModifyRequest, 0, len(result.Results))
			for i, r := range result.Results {
				items = append(items, &ua.MonitoredItemModifyRequest{MonitoredItemID: r.MonitoredItemID, RequestedParameters: &ua.MonitoringParameters{ClientHandle: uint32(i), SamplingInterval: float64(desired / time.Millisecond), QueueSize: 64, DiscardOldest: true}})
			}
			modified, err := sub.ModifyMonitoredItems(ctx, ua.TimestampsToReturnBoth, items...)
			if err != nil {
				return err
			}
			for _, r := range modified.Results {
				if r.StatusCode != ua.StatusOK {
					return fmt.Errorf("OPC-UA sampling update: %s", r.StatusCode)
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		case notification, ok := <-notifications:
			if !ok {
				return fmt.Errorf("OPC-UA notification stream closed")
			}
			if notification == nil {
				continue
			}
			if notification.Error != nil {
				return notification.Error
			}
			if changes, ok := notification.Value.(*ua.DataChangeNotification); ok {
				for _, item := range changes.MonitoredItems {
					if item != nil && int(item.ClientHandle) < len(nodes) {
						emit(nodes[item.ClientHandle], observation(item.Value))
					}
				}
			}
		}
	}
}
func (c *nativeClient) SetCollectionFactor(factor int64) error {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	if c.rate == nil {
		c.rate = make(chan int64, 1)
	}
	select {
	case <-c.rate:
	default:
	}
	c.rate <- factor
	return nil
}

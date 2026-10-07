package app

import (
	"bytes"
	"context"
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
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/platform/internal/cloudsync"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type controlledFeedbackClient struct {
	dt.DataTransferServiceClient
	lost          sync.Map
	cancelEntered chan string
	cancelRelease chan struct{}
}

func (c *controlledFeedbackClient) SendCommand(ctx context.Context, msg *dt.DeviceMessage, opts ...grpc.CallOption) (*dt.CommandResponsePayload, error) {
	response, err := c.DataTransferServiceClient.SendCommand(ctx, msg, opts...)
	if err != nil {
		return response, err
	}
	if strings.HasPrefix(msg.CommandId, "recover-") && strings.HasSuffix(msg.CommandId, ":first") {
		if _, loaded := c.lost.LoadOrStore(msg.CommandId, true); !loaded {
			return nil, status.Error(codes.Unavailable, "controlled reply loss after durable device SUCCESS")
		}
	}
	if strings.HasPrefix(msg.CommandId, "cancel-") && strings.HasSuffix(msg.CommandId, ":first") {
		select {
		case c.cancelEntered <- msg.CommandId:
		default:
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.cancelRelease:
		}
	}
	return response, nil
}

type controlledLink struct {
	base            http.RoundTripper
	offline         atomic.Bool
	loseNext        atomic.Bool
	reverseReceipts atomic.Bool
	reversed        atomic.Bool
	loseReceipt     atomic.Bool
	receiptLost     atomic.Bool
}

func (l *controlledLink) RoundTrip(r *http.Request) (*http.Response, error) {
	if l.offline.Load() {
		return nil, errors.New("controlled cloud link offline")
	}
	loseReceipt := false
	if l.reverseReceipts.Load() || l.loseReceipt.Load() {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body.Close()
		var request cloudsync.Request
		if err = json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		for _, delivery := range request.Deliveries {
			if delivery.Kind == "cloud_operation_receipt" && l.loseReceipt.CompareAndSwap(true, false) {
				loseReceipt = true
			}
		}
		if l.reverseReceipts.Load() && len(request.Deliveries) > 1 {
			for i, j := 0, len(request.Deliveries)-1; i < j; i, j = i+1, j-1 {
				request.Deliveries[i], request.Deliveries[j] = request.Deliveries[j], request.Deliveries[i]
			}
			raw, err = json.Marshal(request)
			if err != nil {
				return nil, err
			}
			l.reverseReceipts.Store(false)
			l.reversed.Store(true)
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
	}
	response, err := l.base.RoundTrip(r)
	if err == nil && (loseReceipt || l.loseNext.CompareAndSwap(true, false)) {
		if loseReceipt {
			l.receiptLost.Store(true)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return nil, errors.New("controlled acknowledgement loss after cloud commit")
	}
	return response, err
}

type controlApplications struct {
	cloud, edge           *Application
	ctx                   context.Context
	cancel                context.CancelFunc
	done                  []chan error
	root                  string
	cloudURL, edgeURL     string
	cloudToken, edgeToken string
	password              string
	device                *exec.Cmd
	deviceDone            chan error
	feedback              *controlledFeedbackClient
	link                  *controlledLink
	driver                *httptest.Server
	engineer, leader      identity.Principal
}

func fixtureWrite(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func freeAddress(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := l.Addr().String()
	l.Close()
	return address
}
func fixtureTLS(t *testing.T, dir string) (ca, cert, key, edgeCert, edgeKey string, edgeIdentity *x509.Certificate) {
	t.Helper()
	rootKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Controlled integration CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(4 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	ca = filepath.Join(dir, "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}), 0600); err != nil {
		t.Fatal(err)
	}
	makeCertificate := func(name string, isClient bool) (string, string, *x509.Certificate) {
		private, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		c := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: root.NotBefore, NotAfter: root.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		if isClient {
			c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
			u, _ := url.Parse("spiffe://smartfactory/edge/edge-a")
			c.URIs = []*url.URL{u}
		}
		der, e := x509.CreateCertificate(rand.Reader, c, root, &private.PublicKey, rootKey)
		if e != nil {
			t.Fatal(e)
		}
		p, k := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key")
		raw, _ := x509.MarshalPKCS8PrivateKey(private)
		if e = os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), 0600); e != nil {
			t.Fatal(e)
		}
		parsed, _ := x509.ParseCertificate(der)
		return p, k, parsed
	}
	cert, key, _ = makeCertificate("cloud", false)
	edgeCert, edgeKey, edgeIdentity = makeCertificate("edge-a", true)
	return
}
func controlApplicationFixture(t *testing.T, root string) *controlApplications {
	t.Helper()
	binary := os.Getenv("SF_CONTROL_DEVICE_BINARY")
	if binary == "" {
		t.Skip("actual DataTransfer controlled fixture binary required")
	}
	if root == "" {
		root = t.TempDir()
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &controlApplications{ctx: ctx, cancel: cancel, root: root}
	var random [24]byte
	rand.Read(random[:])
	f.password = base64.RawURLEncoding.EncodeToString(random[:])
	f.device = exec.Command(binary, "-test.run=^TestControlledControlDeviceFixture$", "-test.timeout=2h", "-test.v")
	f.device.Env = append(os.Environ(), "SF_CONTROL_DEVICE_FIXTURE="+root)
	log, err := os.OpenFile(filepath.Join(root, "device.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.device.Stdout = log
	f.device.Stderr = log
	if err = f.device.Start(); err != nil {
		t.Fatal(err)
	}
	f.deviceDone = make(chan error, 1)
	go func() { f.deviceDone <- f.device.Wait(); log.Close() }()
	t.Cleanup(func() { f.stop(t) })
	var ready struct {
		Address string `json:"address"`
	}
	waitControl(t, "device listener", func() bool {
		raw, e := os.ReadFile(filepath.Join(root, "device-ready.json"))
		return e == nil && json.Unmarshal(raw, &ready) == nil && ready.Address != ""
	})
	ca, cert, key, edgeCert, edgeKey, edgeIdentity := fixtureTLS(t, root)
	cloudAddress, edgeAddress, syncAddress := freeAddress(t), freeAddress(t), freeAddress(t)
	f.cloudURL = "http://" + cloudAddress
	f.edgeURL = "http://" + edgeAddress
	static := os.Getenv("SF_CONTROL_STATIC_DIR")
	f.cloud, err = Open(ctx, Options{Mode: "cloud", NodeID: "cloud", Address: cloudAddress, DSN: filepath.Join(root, "cloud.db"), KeyFile: filepath.Join(root, "cloud-master.key"), BootstrapPassword: f.password, SyncListen: syncAddress, TLSCA: ca, TLSCertificate: cert, TLSKey: key, StaticDir: static})
	if err != nil {
		t.Fatal(err)
	}
	f.edge, err = Open(ctx, Options{Mode: "edge", NodeID: "edge-a", Address: edgeAddress, DSN: filepath.Join(root, "edge.db"), KeyFile: filepath.Join(root, "edge-master.key"), BootstrapPassword: f.password, DataTransferAddress: ready.Address, SyncURL: "https://" + syncAddress, TLSCA: ca, TLSCertificate: edgeCert, TLSKey: edgeKey, CloudSigningKey: base64.StdEncoding.EncodeToString(f.cloud.Store.SignKey.Public().(ed25519.PublicKey)), StaticDir: static})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Application{f.cloud, f.edge} {
		doc, err := a.Store.Get(ctx, "parameter", "control.approval_ttl_ms")
		if err != nil {
			t.Fatal(err)
		}
		parameter, err := store.Decode[configcenter.Parameter](doc)
		if err != nil {
			t.Fatal(err)
		}
		parameter.Value = 3600000
		if _, err = a.Server.Config.Put(ctx, model.Actor{UserID: "admin"}, parameter, doc.Version); err != nil {
			t.Fatal(err)
		}
		if err = a.Server.Config.ApplyPolicy(ctx); err != nil {
			t.Fatal(err)
		}
	}
	public, err := f.edge.Server.Identity.EncryptionPublicKey()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(edgeIdentity.Raw)
	registration, _ := json.Marshal(cloudsync.Registration{CertificateSHA256: hex.EncodeToString(fingerprint[:]), AuditPublicKey: base64.StdEncoding.EncodeToString(f.edge.Store.SignKey.Public().(ed25519.PublicKey)), EncryptionPublicKey: public})
	for _, entity := range []model.Entity{{ID: "factory", Kind: "asset", Name: "验收工厂", Status: "active", Version: 1}, {ID: "edge-a", Kind: "edge", ParentID: "factory", Name: "验收边缘", Status: "active", Version: 1, Config: registration}} {
		if _, err = f.cloud.Store.Put(ctx, "entity", entity.ID, 0, entity); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range []struct{ id, role string }{{"engineer", "engineer"}, {"leader", "leader"}} {
		_, err = f.cloud.Server.Identity.CreateUser(ctx, model.Actor{UserID: "admin"}, model.User{ID: spec.id, Name: spec.id, Login: spec.id, Active: true, Roles: []string{spec.role}, Resources: []string{"*"}, DepartmentID: "engineering"}, f.password, "", 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	entity := model.Entity{ID: "device-grpc", Name: "寄存器模拟设备", Kind: "device", EdgeID: "edge-a", ParentID: "factory", Status: "approved", Version: 1}
	if _, err = f.edge.Store.Put(ctx, "entity", entity.ID, 0, entity); err != nil {
		t.Fatal(err)
	}
	definition := model.Definition{ID: "control-fixture", Name: "控制执行验收策略", Kind: "strategy", SchemaVersion: model.ContractVersion, GroupID: "factory", Status: "published", Version: 1, Policy: model.Policy{EdgeIDs: []string{"edge-a"}, RiskCategory: "business", RiskLevel: 1, Steps: []model.Step{{ID: "first", DeviceID: entity.ID, EdgeID: "edge-a", Action: "set_speed", Params: map[string]string{"speed": "321"}, TimeoutMS: 120000}, {ID: "remaining", DeviceID: entity.ID, EdgeID: "edge-a", Action: "set_speed", Params: map[string]string{"speed": "654"}, TimeoutMS: 120000}}}}
	definition.Nodes = []model.Node{{ID: "manual-input", Type: "input"}}
	definition.Selector = model.Selector{DeviceIDs: []string{entity.ID}, Keys: []string{"fixture.manual.only"}}
	if _, err = f.cloud.Store.Put(ctx, "definition", definition.ID, 0, definition); err != nil {
		t.Fatal(err)
	}
	if err = f.cloud.Server.Engine.PrepareDefinition(ctx, definition); err != nil {
		t.Fatal(err)
	}
	f.feedback = &controlledFeedbackClient{DataTransferServiceClient: f.edge.Bridge.Client, cancelEntered: make(chan string, 20), cancelRelease: make(chan struct{})}
	f.edge.Bridge.Client = f.feedback
	f.link = &controlledLink{base: f.edge.SyncClient.HTTP.Transport}
	f.edge.SyncClient.HTTP.Transport = f.link
	for _, a := range []*Application{f.cloud, f.edge} {
		done := make(chan error, 1)
		f.done = append(f.done, done)
		go func() { done <- a.Run(ctx) }()
	}
	for _, base := range []string{f.cloudURL, f.edgeURL} {
		waitControl(t, "application HTTP", func() bool {
			r, e := http.Get(base + "/health")
			if e != nil {
				return false
			}
			r.Body.Close()
			return r.StatusCode == 200
		})
	}
	waitControl(t, "signed permission bundle and definition", func() bool {
		_, e := f.edge.Store.Get(ctx, "user", "engineer")
		_, d := f.edge.Store.Get(ctx, "definition", definition.ID)
		return e == nil && d == nil
	})
	f.cloudToken, _, err = f.cloud.Server.Identity.Login(ctx, "engineer", f.password, "", false, "control-fixture")
	if err != nil {
		t.Fatal(err)
	}
	f.edgeToken, _, err = f.edge.Server.Identity.Login(ctx, "engineer", f.password, "", true, "control-fixture")
	if err != nil {
		t.Fatal(err)
	}
	f.engineer, err = f.edge.Server.Identity.Authenticate(ctx, f.edgeToken)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := f.edge.Store.Get(ctx, "user", "leader")
	user, _ := store.Decode[model.User](doc)
	f.leader = identity.Principal{User: user, Actor: model.Actor{UserID: user.ID}, Local: true}
	var releaseOnce sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("POST /offline", func(w http.ResponseWriter, r *http.Request) { f.link.offline.Store(true); w.WriteHeader(204) })
	mux.HandleFunc("POST /online", func(w http.ResponseWriter, r *http.Request) { f.link.offline.Store(false); w.WriteHeader(204) })
	mux.HandleFunc("POST /release-cancel", func(w http.ResponseWriter, r *http.Request) {
		releaseOnce.Do(func() { close(f.feedback.cancelRelease) })
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"cloud_link_offline": f.link.offline.Load(), "physical_actions": f.counts(t)})
	})
	f.driver = httptest.NewServer(mux)
	fixtureWrite(t, filepath.Join(root, "private-access.json"), map[string]any{"cloud_url": f.cloudURL, "edge_url": f.edgeURL, "driver_url": f.driver.URL, "username": "engineer", "password": f.password, "cloud_token": f.cloudToken, "edge_token": f.edgeToken, "device_binary": binary, "device_pid": f.device.Process.Pid, "app_fixture_pid": os.Getpid(), "root": root})
	return f
}
func waitControl(t *testing.T, label string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timed out: " + label)
}
func (f *controlApplications) stop(t *testing.T) {
	f.cancel()
	for _, done := range f.done {
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("application shutdown timed out")
		}
	}
	f.done = nil
	for _, a := range []*Application{f.edge, f.cloud} {
		if a != nil {
			a.Close()
		}
	}
	if f.driver != nil {
		f.driver.Close()
	}
	if f.device != nil && f.device.Process != nil {
		os.WriteFile(filepath.Join(f.root, "device-stop"), nil, 0600)
		select {
		case <-f.deviceDone:
		case <-time.After(3 * time.Second):
			f.device.Process.Kill()
			<-f.deviceDone
		}
		f.device = nil
	}
}
func (f *controlApplications) counts(t *testing.T) map[string]int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, "physical-actions.json"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]int{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Commands map[string]int `json:"commands"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Commands
}
func (f *controlApplications) approved(t *testing.T, id string) model.Execution {
	t.Helper()
	ctx := f.ctx
	s := f.edge.Server.Control
	req, err := s.Create(ctx, f.engineer, "control-fixture", map[string]string{}, false, id)
	if err != nil {
		t.Fatal(err)
	}
	req, err = s.Approve(ctx, f.engineer, id, "engineer")
	if err != nil {
		t.Fatal(err)
	}
	req, err = s.Approve(ctx, f.leader, id, "leader")
	if err != nil || req.Status != "approved" {
		t.Fatal(req, err)
	}
	return req
}
func (f *controlApplications) dispatch(t *testing.T, id string) model.Execution {
	t.Helper()
	req := f.approved(t, id)
	req, err := f.edge.Server.Control.Dispatch(f.ctx, f.engineer, req.DownlinkID)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
func (f *controlApplications) awaitState(t *testing.T, a *Application, id, state string) model.Execution {
	t.Helper()
	var req model.Execution
	waitControl(t, "execution "+id+" "+state, func() bool {
		var err error
		req, err = a.Server.Control.Get(f.ctx, id)
		return err == nil && req.Status == state
	})
	return req
}
func (f *controlApplications) request(t *testing.T, base, token, method, path string, body any) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(f.ctx, method, base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("traceparent", "00-12345678901234567890123456789012-2222222222222222-01")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}

func TestControlledCloudEdgeControlIntervention(t *testing.T) {
	var traceMu sync.Mutex
	var exportedSpans []*tracepb.Span
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			raw, err := io.ReadAll(r.Body)
			var exported collectortrace.ExportTraceServiceRequest
			if err != nil || proto.Unmarshal(raw, &exported) != nil {
				t.Error("invalid trace export", err)
			}
			traceMu.Lock()
			for _, resource := range exported.ResourceSpans {
				for _, scope := range resource.ScopeSpans {
					exportedSpans = append(exportedSpans, scope.Spans...)
				}
			}
			traceMu.Unlock()
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer collector.Close()
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_always_on")
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "1000")
	f := controlApplicationFixture(t, os.Getenv("SF_CONTROL_INTEGRATION_OUTPUT"))
	f.dispatch(t, "recover-integration")
	unknown := f.awaitState(t, f.edge, "recover-integration", "result_unknown")
	f.awaitState(t, f.cloud, unknown.DownlinkID, "result_unknown")
	code, raw := f.request(t, f.cloudURL, f.cloudToken, "GET", "/api/sf/v1/executions/"+unknown.DownlinkID, nil)
	var detail model.ExecutionDetail
	if err := json.Unmarshal(raw, &detail); err != nil || code != 200 {
		t.Fatal(code, string(raw), err)
	}
	f.link.offline.Store(true)
	input := model.ExecutionAction{ExpectedVersion: detail.Execution.Version, ExpectedSourceVersion: &detail.SourceVersion, OperationID: "actual-cloud-reconcile", Reason: "通过原始设备日志核对"}
	code, raw = f.request(t, f.cloudURL, f.cloudToken, "POST", "/api/sf/v1/executions/"+unknown.DownlinkID+"/reconcile", input)
	var pending model.ControlOperation
	if err := json.Unmarshal(raw, &pending); err != nil || code != 202 || pending.Status != "pending" {
		t.Fatal(code, string(raw), err)
	}
	code, raw = f.request(t, f.cloudURL, f.cloudToken, "POST", "/api/sf/v1/executions/"+unknown.DownlinkID+"/reconcile", input)
	var duplicate model.ControlOperation
	if err := json.Unmarshal(raw, &duplicate); err != nil || code != 202 || duplicate.ID != pending.ID || duplicate.Version != pending.Version {
		t.Fatal(code, string(raw), err)
	}
	changed := input
	changed.Reason = "changed request body"
	code, _ = f.request(t, f.cloudURL, f.cloudToken, "POST", "/api/sf/v1/executions/"+unknown.DownlinkID+"/reconcile", changed)
	if code != 409 {
		t.Fatal("operation identifier content conflict", code)
	}
	current, _ := f.cloud.Server.Control.Get(f.ctx, unknown.DownlinkID)
	if current.Status != "result_unknown" || current.Version != detail.Execution.Version {
		t.Fatal(current)
	}
	f.link.loseNext.Store(true)
	f.link.reverseReceipts.Store(true)
	f.link.loseReceipt.Store(true)
	f.link.offline.Store(false)
	ready := f.awaitState(t, f.edge, unknown.DownlinkID, "ready_to_resume")
	f.awaitState(t, f.cloud, unknown.DownlinkID, "ready_to_resume")
	waitControl(t, "completed operation receipt", func() bool {
		op, err := f.cloud.Server.Control.Operation(f.ctx, identity.Principal{User: model.User{ID: "engineer"}}, pending.ID)
		return err == nil && op.Status == "completed"
	})
	evidence, err := f.edge.Store.ExecutionEvidence(f.ctx, ready.DownlinkID)
	if err != nil || len(evidence) != 1 || !evidence[0].Trusted || !strings.HasPrefix(evidence[0].Source, "datatransfer.command_journal:") {
		t.Fatal(evidence, err)
	}
	cloudEvidence, err := f.cloud.Store.ExecutionEvidence(f.ctx, ready.DownlinkID)
	if err != nil || store.Hash(cloudEvidence) != store.Hash(evidence) {
		t.Fatal("cloud original-command evidence differs from edge", cloudEvidence, evidence, err)
	}
	verifiedDetails := map[string]model.ExecutionDetail{}
	verifyFeedbackDetails := func(id string, expected []model.CommandEvidence) {
		for _, target := range []struct{ name, url, token string }{{"cloud", f.cloudURL, f.cloudToken}, {"edge", f.edgeURL, f.edgeToken}} {
			code, raw := f.request(t, target.url, target.token, "GET", "/api/sf/v1/executions/"+id, nil)
			var detail model.ExecutionDetail
			if err := json.Unmarshal(raw, &detail); err != nil || code != 200 || store.Hash(detail.Evidence) != store.Hash(expected) {
				t.Fatal("HTTP detail evidence differs", target.name, code, string(raw), err)
			}
			feedback := 0
			for _, entry := range detail.Timeline {
				if entry.Kind == "feedback" {
					feedback++
					if entry.Evidence == nil || entry.Evidence.ID != expected[0].ID || entry.TraceID != expected[0].TraceID || entry.Source != expected[0].Source {
						t.Fatal("HTTP feedback lost its source/trace", entry)
					}
				}
			}
			if feedback != len(expected) {
				t.Fatal("HTTP feedback missing or duplicated", target.name, feedback)
			}
			verifiedDetails[id+":"+target.name] = detail
		}
	}
	verifyFeedbackDetails(ready.DownlinkID, evidence)
	if count := f.counts(t)[unknown.DownlinkID+":first"]; count != 1 {
		t.Fatal(count)
	}
	code, raw = f.request(t, f.edgeURL, f.edgeToken, "POST", "/api/sf/v1/executions/"+ready.DownlinkID+"/resume", model.ExecutionAction{ExpectedVersion: ready.Version, Reason: "人工授权剩余步骤", OperationID: "actual-edge-resume"})
	if code != 200 {
		t.Fatal(code, string(raw))
	}
	completed := f.awaitState(t, f.edge, ready.DownlinkID, "completed")
	f.awaitState(t, f.cloud, ready.DownlinkID, "completed")
	if len(completed.Steps) != 2 || f.counts(t)[ready.DownlinkID+":first"] != 1 || f.counts(t)[ready.DownlinkID+":remaining"] != 1 {
		t.Fatal(completed, f.counts(t))
	}
	verifyFeedbackDetails(completed.DownlinkID, evidence)
	f.dispatch(t, "cancel-integration")
	select {
	case <-f.feedback.cancelEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("cancel command did not start")
	}
	running, _ := f.edge.Server.Control.Get(f.ctx, "cancel-integration")
	code, raw = f.request(t, f.edgeURL, f.edgeToken, "POST", "/api/sf/v1/executions/"+running.DownlinkID+"/cancel", model.ExecutionAction{ExpectedVersion: running.Version, Reason: "停止后续动作", OperationID: "actual-edge-cancel"})
	if code != 200 {
		t.Fatal(code, string(raw))
	}
	unknownCancel := f.awaitState(t, f.edge, running.DownlinkID, "cancelled_result_unknown")
	response, err := http.Post(f.driver.URL+"/release-cancel", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	cancelled := f.awaitState(t, f.edge, running.DownlinkID, "cancelled")
	f.awaitState(t, f.cloud, running.DownlinkID, "cancelled")
	if len(cancelled.Steps) != 1 || cancelled.Steps[0].Status != "SUCCESS" || f.counts(t)[running.DownlinkID+":remaining"] != 0 {
		t.Fatal(cancelled, f.counts(t))
	}
	cancelEvidence, err := f.edge.Store.ExecutionEvidence(f.ctx, running.DownlinkID)
	if err != nil || len(cancelEvidence) != 1 || !cancelEvidence[0].Trusted || cancelEvidence[0].Source != "control.action_journal:edge-a" || cancelled.Steps[0].EvidenceID != cancelEvidence[0].ID {
		t.Fatal("late original command evidence missing", cancelEvidence, err)
	}
	verifyFeedbackDetails(cancelled.DownlinkID, cancelEvidence)
	if !f.link.reversed.Load() || !f.link.receiptLost.Load() {
		t.Fatal("controlled delivery conditions were not exercised", f.link.reversed.Load(), f.link.receiptLost.Load())
	}
	rejections := map[string]model.ControlOperation{}
	for _, scenario := range []string{"expired", "source_version_changed", "permission_revoked"} {
		id := "recover-" + scenario
		f.dispatch(t, id)
		original := f.awaitState(t, f.edge, id, "result_unknown")
		f.awaitState(t, f.cloud, id, "result_unknown")
		code, raw = f.request(t, f.cloudURL, f.cloudToken, "GET", "/api/sf/v1/executions/"+id, nil)
		var currentDetail model.ExecutionDetail
		if err = json.Unmarshal(raw, &currentDetail); err != nil || code != 200 {
			t.Fatal(code, string(raw), err)
		}
		f.link.offline.Store(true)
		action := model.ExecutionAction{ExpectedVersion: currentDetail.Execution.Version, ExpectedSourceVersion: &currentDetail.SourceVersion, OperationID: "actual-" + scenario, Reason: "检查边缘收到请求时的有效性"}
		if scenario == "expired" {
			action.DeadlineMS = time.Now().Add(250 * time.Millisecond).UnixMilli()
		}
		code, raw = f.request(t, f.cloudURL, f.cloudToken, "POST", "/api/sf/v1/executions/"+id+"/reconcile", action)
		if code != 202 {
			t.Fatal(code, string(raw))
		}
		switch scenario {
		case "expired":
			waitControl(t, "operation deadline while offline", func() bool { return time.Now().UnixMilli() > action.DeadlineMS })
		case "source_version_changed":
			code, raw = f.request(t, f.edgeURL, f.edgeToken, "POST", "/api/sf/v1/executions/"+id+"/cancel", model.ExecutionAction{ExpectedVersion: original.Version, OperationID: "local-version-change", Reason: "现场取消先于云端请求"})
			if code != 200 {
				t.Fatal(code, string(raw))
			}
		case "permission_revoked":
			doc, e := f.cloud.Store.Get(f.ctx, "user", "engineer")
			if e != nil {
				t.Fatal(e)
			}
			u, e := store.Decode[model.User](doc)
			if e != nil {
				t.Fatal(e)
			}
			u.Active = false
			if _, e = f.cloud.Store.Put(f.ctx, "user", u.ID, doc.Version, u); e != nil {
				t.Fatal(e)
			}
		}
		f.link.offline.Store(false)
		var result model.ControlOperation
		waitControl(t, scenario+" operation result", func() bool {
			doc, e := f.cloud.Store.Get(f.ctx, "control_operation", action.OperationID)
			if e != nil {
				return false
			}
			result, e = store.Decode[model.ControlOperation](doc)
			return e == nil && (result.Status == "expired" || result.Status == "rejected")
		})
		want := "rejected"
		if scenario == "expired" {
			want = "expired"
		}
		if result.Status != want || f.counts(t)[id+":first"] != 1 || f.counts(t)[id+":remaining"] != 0 {
			t.Fatal(scenario, result, f.counts(t))
		}
		rejections[scenario] = result
	}
	transitions, err := f.edge.Store.ExecutionTransitions(f.ctx, completed.DownlinkID)
	if err != nil {
		t.Fatal(err)
	}
	pid := f.device.Process.Pid
	f.stop(t)
	f.cloud, f.edge = nil, nil
	traceMu.Lock()
	defer traceMu.Unlock()
	traces := map[string]bool{}
	operations := map[string]bool{}
	var traceEvidence []map[string]any
	for _, span := range exportedSpans {
		traces[hex.EncodeToString(span.TraceId)] = true
		operations[span.Name] = true
		if !strings.HasPrefix(span.Name, "control.") {
			continue
		}
		attributes := map[string]string{}
		for _, attr := range span.Attributes {
			if strings.HasPrefix(attr.Key, "smartfactory.") {
				attributes[attr.Key] = attr.Value.GetStringValue()
			}
		}
		traceEvidence = append(traceEvidence, map[string]any{"name": span.Name, "trace_id": hex.EncodeToString(span.TraceId), "span_id": hex.EncodeToString(span.SpanId), "parent_span_id": hex.EncodeToString(span.ParentSpanId), "attributes": attributes, "status": span.Status})
		if span.Name == "control.action" || span.Name == "control.result_lookup" {
			for _, field := range []string{"smartfactory.request_id", "smartfactory.command_id", "smartfactory.definition_id", "smartfactory.entity_id", "smartfactory.entity_revision"} {
				if attributes[field] == "" {
					t.Error("missing control trace identity", span.Name, field)
				}
			}
		}
	}
	for _, name := range []string{"http.request", "control.request", "control.approve", "control.dispatch", "control.run", "control.wait", "control.action", "control.cancel", "control.reconcile", "control.result_lookup", "control.resume", "store.transaction"} {
		if !operations[name] {
			t.Error("missing actual OTLP span", name)
		}
	}
	for _, transition := range transitions {
		if transition.TraceID == "" || !traces[transition.TraceID] {
			t.Error("transition has no exported trace", transition)
		}
	}
	if !traces[evidence[0].TraceID] {
		t.Error("command evidence has no exported trace", evidence[0].TraceID)
	}
	if !traces[cancelEvidence[0].TraceID] {
		t.Error("late command evidence has no exported trace", cancelEvidence[0].TraceID)
	}
	fixtureWrite(t, filepath.Join(f.root, "cloud-edge-evidence-details.json"), verifiedDetails)
	fixtureWrite(t, filepath.Join(f.root, "control-traces.json"), traceEvidence)
	fixtureWrite(t, filepath.Join(f.root, "verified.json"), map[string]any{"cloud_edge_actual_applications": true, "mutual_tls_and_signed_exchange": true, "datatransfer_process_pid": pid, "cloud_http_pending_status": 202, "response_loss_replayed": true, "actual_out_of_order_receipts": f.link.reversed.Load(), "receipt_response_lost_and_retried": f.link.receiptLost.Load(), "same_operation_content_conflict_http": 409, "rejected_operations": rejections, "unknown": unknown, "ready_to_resume": ready, "completed": completed, "cancelled_result_unknown": unknownCancel, "cancelled": cancelled, "command_evidence": evidence, "cloud_command_evidence": cloudEvidence, "cancel_late_command_evidence": cancelEvidence, "cloud_edge_http_feedback_evidence_equal": true, "physical_actions": f.counts(t), "linked_transitions": transitions, "otlp_control_span_count": len(traceEvidence)})
	t.Logf("actual cloud+edge Application.Run and separate DataTransfer runtime over gRPC; mTLS pending202, offline + acknowledgement loss, original read-only journal=%s; final counts=%v; evidence=%s", evidence[0].Source, f.counts(t), filepath.Join(f.root, "verified.json"))
}

func TestControlBrowserFixture(t *testing.T) {
	root := os.Getenv("SF_CONTROL_BROWSER_FIXTURE")
	if root == "" {
		t.Skip("browser fixture output directory required")
	}
	f := controlApplicationFixture(t, root)
	f.dispatch(t, "recover-browser")
	f.awaitState(t, f.edge, "recover-browser", "result_unknown")
	f.awaitState(t, f.cloud, "recover-browser", "result_unknown")
	f.approved(t, "cancel-browser")
	fixtureWrite(t, filepath.Join(root, "browser-ready.json"), map[string]any{"cloud_url": f.cloudURL, "edge_url": f.edgeURL, "driver_url": f.driver.URL, "unknown_execution": "recover-browser", "approved_cancel_execution": "cancel-browser", "access_file": filepath.Join(root, "private-access.json"), "stop_file": filepath.Join(root, "browser-stop")})
	t.Log("controlled browser applications ready; credentials stay in the private access file")
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if _, err := os.Stat(filepath.Join(root, "browser-stop")); err == nil {
			return
		}
	}
}

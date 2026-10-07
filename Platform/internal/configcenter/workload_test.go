package configcenter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func workloadFixture(t *testing.T) (*Service, *Service, *httptest.Server, *Subscriber, nodeidentity.Principal) {
	t.Helper()
	ctx := context.Background()
	makeService := func(name string) *Service {
		dsn := filepath.Join(t.TempDir(), name+".db")
		if os.Getenv("SF_TEST_NODE_CONFIGURATION_PG") == "1" {
			dsn, _ = testdb.Postgres(t, "node_configuration_"+strings.ReplaceAll(name, "-", "_"))
		}
		db, err := store.Open(ctx, dsn, name, make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		db.DB.SetMaxOpenConns(3)
		db.DB.SetMaxIdleConns(1)
		t.Cleanup(func() { db.Close() })
		return &Service{Store: db, Identity: &identity.Manager{Store: db, Master: make([]byte, 32)}}
	}
	remote, local := makeService("config"), makeService("edge-a")
	remote.Workloads = &nodeidentity.Service{Store: remote.Store, Cipher: remote.Identity}
	params := []Parameter{{ID: "control.start_ttl_ms", Program: "edge", Dynamic: true, Schema: map[string]any{"type": "integer", "minimum": 1000}, Value: 10000, TargetNodeIDs: []string{"edge-a"}}, {ID: "static.a", Program: "edge", Schema: map[string]any{"type": "integer"}, Value: 1, TargetNodeIDs: []string{"edge-a"}}, {ID: "secret.a", Program: "edge", Dynamic: true, Secret: true, Schema: map[string]any{"type": "string"}, Value: "private-fixture-parameter-value", TargetNodeIDs: []string{"edge-a"}}, {ID: "queue.capacity", Program: "gateway", Dynamic: true, Schema: map[string]any{"type": "integer", "minimum": 1000}, Value: 3000, TargetNodeIDs: []string{"edge-b"}}}
	for _, p := range params {
		if _, err := remote.Put(ctx, model.Actor{}, p, 0); err != nil {
			t.Fatal(err)
		}
	}
	w := model.WorkloadIdentity{ID: "workload-a", NodeID: "edge-a", Program: "edge", Purpose: "runtime", Enabled: true, Capabilities: []string{"config.v2", "config.read", "config.report", "credential.resolve", "release"}, ParameterIDs: []string{"control.start_ttl_ms", "static.a", "secret.a"}, ConnectorIDs: []string{}}
	w, err := remote.Workloads.Put(ctx, model.Actor{}, w, 0)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	if _, err = remote.Workloads.Rotate(ctx, model.Actor{}, w.ID, token, w.Version); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	remote.RegisterInternal(mux, "legacy-only")
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	c := &Subscriber{URL: server.URL, WorkloadToken: token, NodeID: "edge-a", Local: local, OnApply: local.ApplyPolicy, InstanceID: "instance-a-one"}
	if err = c.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := remote.Workloads.Authenticate(ctx, token, c.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	return remote, local, server, c, p
}

func TestWorkloadStreamSlowHTTP1AndHTTP2ClientsReleaseHandler(t *testing.T) {
	remote, _, _, _, p := workloadFixture(t)
	ctx := context.Background()
	param := Parameter{ID: "slow.content", Program: "edge", Dynamic: true, Schema: map[string]any{"type": "string"}, Value: strings.Repeat("x", 8<<20), TargetNodeIDs: []string{"edge-a"}}
	if _, e := remote.Put(ctx, model.Actor{}, param, 0); e != nil {
		t.Fatal(e)
	}
	w := p.Identity
	w.ParameterIDs = append(w.ParameterIDs, param.ID)
	if _, e := remote.Workloads.Put(ctx, model.Actor{}, w, w.Version); e != nil {
		t.Fatal(e)
	}
	for _, protocol := range []string{"http1", "http2"} {
		t.Run(protocol, func(t *testing.T) {
			mux := http.NewServeMux()
			remote.RegisterWorkloads(mux)
			done := make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(done); mux.ServeHTTP(w, r) }))
			if protocol == "http2" {
				server.EnableHTTP2 = true
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			if protocol == "http1" {
				endpoint, _ := url.Parse(server.URL)
				conn, e := net.DialTimeout("tcp", endpoint.Host, time.Second)
				if e != nil {
					t.Fatal(e)
				}
				defer conn.Close()
				if tcp, ok := conn.(*net.TCPConn); ok {
					tcp.SetReadBuffer(1024)
				}
				if _, e = fmt.Fprintf(conn, "GET /internal/config/v2/stream HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nX-SF-Instance-ID: %s\r\n\r\n", endpoint.Host, strings.Repeat("a", 64), p.InstanceID); e != nil {
					t.Fatal(e)
				}
			} else {
				req, _ := http.NewRequest("GET", server.URL+"/internal/config/v2/stream", nil)
				req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
				req.Header.Set("X-SF-Instance-ID", p.InstanceID)
				res, e := server.Client().Do(req)
				if e != nil {
					t.Fatal(e)
				}
				defer res.Body.Close()
				if res.ProtoMajor != 2 {
					t.Fatal("HTTP/2 was not exercised")
				}
			}
			select {
			case <-done:
			case <-time.After(7 * time.Second):
				t.Fatal("slow client retained subscription handler past bounded write deadline")
			}
		})
	}
	t.Log("real unread HTTP/1 socket and HTTP/2 flow-control blocked stream released handlers within finite write deadlines")
}

func TestWorkloadRevocationBetweenConnectionsClearsActualCachedPolicy(t *testing.T) {
	for _, stage := range []string{"cached-startup", "stream-reconnect"} {
		t.Run(stage, func(t *testing.T) {
			remote, local, _, subscriber, principal := workloadFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			parameter := Parameter{ID: "control.start_ttl_ms", Program: "edge", Dynamic: true, Schema: map[string]any{"type": "integer", "minimum": 1000}, Value: 2400, TargetNodeIDs: []string{"edge-a"}}
			if _, e := remote.Put(ctx, model.Actor{}, parameter, 1); e != nil {
				t.Fatal(e)
			}
			envelopes, e := remote.WorkloadSnapshot(ctx, principal)
			if e != nil {
				t.Fatal(e)
			}
			if e = subscriber.ApplyEnvelopes(ctx, envelopes, nil, true); e != nil || local.Store.Policy().StartTTLMS != 2400 {
				t.Fatal("actual validated cache was not established", e)
			}
			var allowStream atomic.Bool
			mux := http.NewServeMux()
			remote.RegisterWorkloads(mux)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/internal/config/v2/stream" && !allowStream.Load() {
					http.Error(w, "temporarily unavailable", 503)
					return
				}
				mux.ServeHTTP(w, r)
			}))
			defer server.Close()
			subscriber.URL = server.URL
			done := make(chan error, 1)
			if stage == "stream-reconnect" {
				go func() { done <- subscriber.Run(ctx) }()
				deadline := time.Now().Add(3 * time.Second)
				for {
					if _, e := local.Store.Get(ctx, "configuration_connection", local.Store.NodeID); e == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("subscriber did not reach reconnect state")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if local.Store.Policy().StartTTLMS != 2400 {
					t.Fatal("temporary stream failure discarded cache")
				}
			}
			workload := principal.Identity
			workload.Enabled = false
			if _, e = remote.Workloads.Put(ctx, model.Actor{}, workload, workload.Version); e != nil {
				t.Fatal(e)
			}
			allowStream.Store(true)
			if stage == "cached-startup" {
				go func() { done <- subscriber.Run(ctx) }()
			}
			deadline := time.Now().Add(3 * time.Second)
			for local.Store.Policy().StartTTLMS != 10000 {
				if time.Now().After(deadline) {
					t.Fatal("explicit revocation kept unauthorized cached actual policy")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if cached, e := local.Store.List(ctx, "configuration_runtime"); e != nil || len(cached) != 0 {
				t.Fatal("revoked managed runtime remained", e)
			}
			cancel()
			if e = <-done; e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestWorkloadConfigurationDynamicFailureStaticRestart(t *testing.T) {
	remote, local, _, c, p := workloadFixture(t)
	ctx := context.Background()
	envelopes, err := remote.WorkloadSnapshot(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 3 {
		t.Fatal("incorrect workload scope", len(envelopes))
	}
	raw, _ := json.Marshal(envelopes)
	if strings.Contains(string(raw), "private-fixture-") {
		t.Fatal("snapshot disclosed credential payload")
	}
	if err = c.ApplyEnvelopes(ctx, envelopes, nil, true); err != nil {
		t.Fatal(err)
	}
	if local.Store.Policy().StartTTLMS != 10000 {
		t.Fatal("runtime policy was not applied")
	}
	params, _ := remote.List(ctx)
	for _, v := range params {
		if v.ID == "control.start_ttl_ms" {
			v.Value = 2000
			if _, err = remote.Put(ctx, model.Actor{}, v, 1); err != nil {
				t.Fatal(err)
			}
		}
		if v.ID == "static.a" {
			v.Value = 2
			if _, err = remote.Put(ctx, model.Actor{}, v, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	envelopes, err = remote.WorkloadSnapshot(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	c.OnApply = func(ctx context.Context) error {
		if e := local.ApplyPolicy(ctx); e != nil {
			return e
		}
		return errors.New("consumer partially applied then rejected")
	}
	if err = c.ApplyEnvelopes(ctx, envelopes, nil, false); err != nil {
		t.Fatal(err)
	}
	if local.Store.Policy().StartTTLMS != 10000 {
		t.Fatal("failed runtime was not rolled back")
	}
	for _, kind := range []string{"control.start_ttl_ms", "static.a"} {
		d, e := remote.Store.Get(ctx, "configuration_report", "workload-a:parameter:"+kind)
		if e != nil {
			t.Fatal(e)
		}
		r, e := store.Decode[model.ConfigurationReport](d)
		if e != nil {
			t.Fatal(e)
		}
		if r.Desired.Version != 2 || r.Prepared.Version != 2 || r.Running.Version != 1 {
			t.Fatal("stage versions were conflated", r)
		}
		if kind == "static.a" && r.State != "restart_required" {
			t.Fatal(r)
		}
		if kind != "static.a" && r.State != "failed" {
			t.Fatal(r)
		}
	}
	c.OnApply = local.ApplyPolicy
	if err = c.ApplyEnvelopes(ctx, envelopes, nil, false); err != nil {
		t.Fatal(err)
	}
	if local.Store.Policy().StartTTLMS != 2000 {
		t.Fatal("dynamic retry did not reach actual policy")
	}
	v, e := local.RuntimeValue(ctx, "static.a")
	if e != nil || fmt.Sprint(v) != "1" {
		t.Fatal("static changed without process startup", v, e)
	}
	// A replacement process has a new authenticated epoch and reads the same DB.
	restarted := &Subscriber{URL: c.URL, WorkloadToken: c.WorkloadToken, InstanceID: "instance-a-two", NodeID: c.NodeID, Local: local, OnApply: local.ApplyPolicy}
	if err = restarted.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	p2, err := remote.Workloads.Authenticate(ctx, c.WorkloadToken, restarted.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	envelopes, err = remote.WorkloadSnapshot(ctx, p2)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.ApplyEnvelopes(ctx, envelopes, nil, true); err != nil {
		t.Fatal(err)
	}
	v, e = local.RuntimeValue(ctx, "static.a")
	if e != nil || fmt.Sprint(v) != "2" {
		t.Fatal("new instance did not apply static", v, e)
	}
	if _, err = remote.WorkloadSnapshot(ctx, p); !errors.Is(err, identity.ErrAuthentication) {
		t.Fatal("old instance remained active", err)
	}
	stats, err := local.RuntimeSnapshot(ctx)
	if err != nil || len(stats) != 3 {
		t.Fatal(stats, err)
	}
	t.Log("independent authenticated dynamic policy success, partial consumer failure rollback, retry, static prepared old running, and replacement process epoch all verified")
}

func TestWorkloadIdentityScopeReportsRevocationAndRotation(t *testing.T) {
	remote, _, server, c, p := workloadFixture(t)
	ctx := context.Background()
	env, err := remote.WorkloadSnapshot(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ApplyEnvelopes(ctx, env, nil, true); err != nil {
		t.Fatal(err)
	}
	w := model.WorkloadIdentity{ID: "workload-b", NodeID: "edge-b", Program: "gateway", Purpose: "gateway-runtime", Enabled: true, Capabilities: []string{"config.v2", "config.read", "config.report", "credential.resolve"}, ParameterIDs: []string{"queue.capacity", "secret.a"}, ConnectorIDs: []string{}}
	w, err = remote.Workloads.Put(ctx, model.Actor{}, w, 0)
	if err != nil {
		t.Fatal(err)
	}
	tokenB := strings.Repeat("b", 64)
	if _, err = remote.Workloads.Rotate(ctx, model.Actor{}, w.ID, tokenB, w.Version); err != nil {
		t.Fatal(err)
	}
	pB, err := remote.Workloads.ActivateInstance(ctx, tokenB, "instance-b-one")
	if err != nil {
		t.Fatal(err)
	}
	other, err := remote.WorkloadSnapshot(ctx, pB)
	if err != nil || len(other) != 1 || other[0].Reference.ID != "queue.capacity" {
		t.Fatal("node and program filtering failed", other, err)
	}
	var secret model.ConfigurationEnvelope
	for _, e := range env {
		if e.Reference.ID == "secret.a" {
			secret = e
		}
	}
	request := model.CredentialResolution{Reference: secret.Reference, CredentialRef: secret.CredentialRef, Purpose: p.Identity.Purpose}
	if _, err = remote.ResolveCredential(ctx, pB, request); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("other workload resolved secret", err)
	}
	request.Purpose = "other-purpose"
	if _, err = remote.ResolveCredential(ctx, p, request); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("purpose mismatch accepted", err)
	}
	request.Purpose = p.Identity.Purpose
	if _, err = remote.ResolveCredential(ctx, p, request); err != nil {
		t.Fatal(err)
	}
	d, err := remote.Store.Get(ctx, "configuration_report", "workload-a:parameter:control.start_ttl_ms")
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.Decode[model.ConfigurationReport](d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = remote.ReportConfiguration(ctx, p, r); err != nil {
		t.Fatal("duplicate acknowledgement failed", err)
	}
	r.NodeID = "edge-b"
	if _, err = remote.ReportConfiguration(ctx, p, r); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("forged node accepted", err)
	}
	r.NodeID = "edge-a"
	r.Sequence++
	r.Desired.Digest = strings.Repeat("0", 64)
	if _, err = remote.ReportConfiguration(ctx, p, r); !errors.Is(err, store.ErrConflict) {
		t.Fatal("same version different digest accepted", err)
	}
	// Keep a real established stream open while scope, credential and enabled state change.
	openStream := func(token, instance string) *bufio.Scanner {
		t.Helper()
		req, _ := http.NewRequest("GET", server.URL+"/internal/config/v2/stream", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-SF-Instance-ID", instance)
		res, e := http.DefaultClient.Do(req)
		if e != nil || res.StatusCode != 200 {
			t.Fatal(e, res)
		}
		t.Cleanup(func() { res.Body.Close() })
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				break
			}
		}
		return scanner
	}
	changed := func(scanner *bufio.Scanner) {
		t.Helper()
		done := make(chan bool, 1)
		go func() {
			for scanner.Scan() {
				if scanner.Text() == "event: identity_changed" {
					done <- true
					return
				}
			}
			done <- false
		}()
		select {
		case okay := <-done:
			if !okay {
				t.Fatal("established stream did not terminate authentication")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("identity change was not applied to established stream")
		}
	}
	scanner := openStream(c.WorkloadToken, c.InstanceID)
	updated, err := remote.Workloads.Lookup(ctx, p.Identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated.ParameterIDs = []string{"control.start_ttl_ms"}
	updated, err = remote.Workloads.Put(ctx, model.Actor{}, updated, updated.Version)
	if err != nil {
		t.Fatal(err)
	}
	changed(scanner)
	p, err = remote.Workloads.Authenticate(ctx, c.WorkloadToken, c.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	env, err = remote.WorkloadSnapshot(ctx, p)
	if err != nil || len(env) != 1 {
		t.Fatal("changed scope was not applied", env, err)
	}
	if _, err = remote.ResolveCredential(ctx, p, request); !errors.Is(err, identity.ErrDenied) {
		t.Fatal("removed secret scope remained usable", err)
	}
	scanner = openStream(c.WorkloadToken, c.InstanceID)
	newToken := strings.Repeat("c", 64)
	updated, err = remote.Workloads.Rotate(ctx, model.Actor{}, updated.ID, newToken, updated.Version)
	if err != nil {
		t.Fatal(err)
	}
	changed(scanner)
	if _, err = remote.Workloads.Authenticate(ctx, c.WorkloadToken, c.InstanceID); !errors.Is(err, identity.ErrAuthentication) {
		t.Fatal("rotated token remained valid", err)
	}
	p, err = remote.Workloads.ActivateInstance(ctx, newToken, "instance-a-rotated")
	if err != nil {
		t.Fatal(err)
	}
	scanner = openStream(newToken, p.InstanceID)
	updated, err = remote.Workloads.Lookup(ctx, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated.Enabled = false
	if _, err = remote.Workloads.Put(ctx, model.Actor{}, updated, updated.Version); err != nil {
		t.Fatal(err)
	}
	changed(scanner)
	if _, err = remote.ResolveCredential(ctx, p, request); !errors.Is(err, identity.ErrAuthentication) {
		t.Fatal("revoked identity resolved secret", err)
	}
	t.Log("two independently authenticated programs: scope/purpose/forged report/duplicate digest/revision/established-stream rotation and revocation verified")
}

func TestFixedReleaseConfigurationTargetSurvivesParallelLatestRead(t *testing.T) {
	remote, _, _, c, p := workloadFixture(t)
	ctx := context.Background()
	v, err := remote.WorkloadSnapshot(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var old model.ConfigurationReference
	for _, e := range v {
		if e.Reference.ID == "control.start_ttl_ms" {
			old = e.Reference
		}
	}
	parameter := Parameter{ID: old.ID, Program: "edge", Dynamic: true, Schema: map[string]any{"type": "integer", "minimum": 1000}, Value: 2000, TargetNodeIDs: []string{"edge-a"}}
	if _, err = remote.Put(ctx, model.Actor{}, parameter, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = remote.GetReleaseConfiguration(ctx, p, []model.ConfigurationReference{old}); err != nil {
		t.Fatal(err)
	}
	latest, err := remote.WorkloadSnapshot(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range latest {
		if e.Reference.ID == old.ID && e.Reference.Version != 2 {
			t.Fatal("read changed current desired")
		}
	}
	if err = remote.Store.Write(ctx, func(tx *store.Tx) error {
		return remote.SetReleaseConfigurationTargetTx(tx, p, []model.ConfigurationReference{old})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = remote.GetReleaseConfiguration(ctx, p, []model.ConfigurationReference{{Kind: "parameter", ID: old.ID, Version: 2}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		fixed, err := remote.WorkloadSnapshot(ctx, p)
		if err != nil || len(fixed) != 1 || fixed[0].Reference != old {
			t.Fatal("live subscription overwrote fixed release", fixed, err)
		}
	}
	if err = c.ApplyEnvelopes(ctx, []model.ConfigurationEnvelope{{Reference: old, NodeID: "edge-a", Program: "edge", Purpose: "runtime", Dynamic: true, Schema: parameter.Schema, Value: 10000}}, nil, true); err != nil {
		t.Fatal(err)
	}
	if err = remote.Store.Write(ctx, func(tx *store.Tx) error {
		return remote.SetReleaseConfigurationTargetTx(tx, p, []model.ConfigurationReference{})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = remote.Store.Get(ctx, "configuration_target", targetID(p.Identity, old)); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("removed release target still permitted old running reports or credential resolution", err)
	}
	if fixed, e := remote.WorkloadSnapshot(ctx, p); e != nil || len(fixed) != 0 {
		t.Fatal("removed release reference returned to latest subscription", e, fixed)
	}
	for _, envelope := range v {
		if envelope.CredentialRef == "" {
			continue
		}
		_, e := remote.ResolveCredential(ctx, p, model.CredentialResolution{Reference: envelope.Reference, CredentialRef: envelope.CredentialRef, Purpose: p.Identity.Purpose})
		if !errors.Is(e, identity.ErrDenied) {
			t.Fatal("removed fixed target still authorized old secret", e)
		}
	}
	t.Log("fixed old release survives latest reads and recurring subscription; explicit startup rollback applied authorized old policy; removed target no longer authorizes old references")
}

package configcenter

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

type registrationFixture struct {
	service *Service
	admin   *sql.DB
	appName string
	mu      sync.Mutex
	proofs  map[string]model.ConnectorRegistration
	status  int
}

func registrationTLS(t *testing.T, handler http.Handler) (*httptest.Server, *http.Client) {
	t.Helper()
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "registration-authority-ca"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	issue := func(name string, serial int64, usage x509.ExtKeyUsage) tls.Certificate {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		raw, e := x509.CreateCertificate(rand.Reader, template, root, key.Public(), caKey)
		if e != nil {
			t.Fatal(e)
		}
		return tls.Certificate{Certificate: [][]byte{raw}, PrivateKey: key}
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{issue("cloud-authority", 2, x509.ExtKeyUsageServerAuth)}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	server.StartTLS()
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{issue("config-service", 3, x509.ExtKeyUsageClientAuth)}}}
	t.Cleanup(transport.CloseIdleConnections)
	t.Cleanup(server.Close)
	return server, &http.Client{Transport: transport, Timeout: 15 * time.Second}
}

func newRegistrationFixture(t *testing.T) *registrationFixture {
	t.Helper()
	f := &registrationFixture{proofs: map[string]model.ConnectorRegistration{}, status: http.StatusOK, appName: fmt.Sprintf("registration_%d", time.Now().UnixNano())}
	dsn := filepath.Join(t.TempDir(), "configuration.db")
	if os.Getenv("SF_TEST_NODE_CONFIGURATION_PG") == "1" {
		dsn, f.admin = testdb.Postgres(t, "connector_registration")
		u, e := url.Parse(dsn)
		if e != nil {
			t.Fatal(e)
		}
		q := u.Query()
		q.Set("application_name", f.appName)
		u.RawQuery = q.Encode()
		dsn = u.String()
	}
	db, err := store.Open(context.Background(), dsn, "config-registration", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.DB.SetMaxOpenConns(3)
	db.DB.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	server, client := registrationTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/authority/connector-metadata" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "config-service" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var in struct {
			References []model.ConfigurationReference `json:"references"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.status != http.StatusOK {
			w.WriteHeader(f.status)
			return
		}
		out := make([]model.ConnectorRegistration, 0, len(in.References))
		for _, ref := range in.References {
			proof, ok := f.proofs[model.ConnectorSecretReference(ref.ID, ref.Version)]
			if ref.Version == 0 {
				for _, candidate := range f.proofs {
					if candidate.Reference.ID == ref.ID && candidate.Reference.Version > proof.Reference.Version {
						proof, ok = candidate, true
					}
				}
			}
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			out = append(out, proof)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	f.service = &Service{Store: db, Identity: &identity.Manager{Store: db, Master: make([]byte, 32)}}
	f.service.Workloads = &nodeidentity.Service{Store: db, Cipher: f.service.Identity, AuthorityURL: server.URL, HTTPClient: client}
	return f
}

func (f *registrationFixture) proof(name string, version, nodeVersion int64) model.ConnectorRegistration {
	c := model.ConnectorConfiguration{ID: "edge-a/" + name, ConnectorID: name, EdgeID: "edge-a", GroupID: "factory", Protocol: "mqtt_device", Version: version, Config: json.RawMessage(`{"kind":"connector","connector_id":"` + name + `","connection":{"url":"tcp://127.0.0.1:1883"},"polling":{"interval_millis":1000}}`), CredentialRef: model.ConnectorSecretReference("edge-a/"+name, version), UpdatedMS: version}
	proof := model.ConnectorRegistration{Reference: model.ConfigurationReference{Kind: "connector", ID: c.ID, Version: version, Digest: ConnectorDigest(c)}, Configuration: c, NodeID: c.EdgeID, NodeVersion: nodeVersion, SourceURL: "https://registered-owner.invalid", CertificateSHA256: strings.Repeat("a", 64)}
	f.mu.Lock()
	f.proofs[model.ConnectorSecretReference(c.ID, version)] = proof
	f.mu.Unlock()
	return proof
}

func (f *registrationFixture) setProof(proof model.ConnectorRegistration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proofs[model.ConnectorSecretReference(proof.Reference.ID, proof.Reference.Version)] = proof
}

func (f *registrationFixture) ensure(t *testing.T, refs ...model.ConfigurationReference) {
	t.Helper()
	if err := f.service.EnsureConnectorVersions(context.Background(), refs); err != nil {
		t.Fatal(err)
	}
}

func (f *registrationFixture) source(t *testing.T, ref model.ConfigurationReference) model.ConnectorRegistration {
	t.Helper()
	d, err := f.service.Store.Get(context.Background(), "configuration_connector_source", model.ConnectorSecretReference(ref.ID, ref.Version))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Decode[model.ConnectorRegistration](d)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func registrationLockKey(kind, id string) int64 {
	sum := sha256.Sum256([]byte("smartfactory/document\x00" + kind + "\x00" + id))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func (f *registrationFixture) concurrent(t *testing.T, kind, id string, a, b []model.ConfigurationReference) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var blocker *sql.Tx
	if f.admin != nil {
		var err error
		blocker, err = f.admin.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		if _, err = blocker.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", registrationLockKey(kind, id)); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 2)
	go func() { done <- f.service.EnsureConnectorVersions(ctx, a) }()
	if blocker != nil {
		deadline := time.Now().Add(8 * time.Second)
		var waiting int
		for waiting < 1 && time.Now().Before(deadline) {
			if _, err := blocker.ExecContext(ctx, "SELECT pg_stat_clear_snapshot()"); err != nil {
				t.Fatal(err)
			}
			if err := blocker.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE l.locktype='advisory' AND NOT l.granted AND a.application_name=$1`, f.appName).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting < 1 {
				time.Sleep(5 * time.Millisecond)
			}
		}
		if waiting != 1 {
			t.Fatal("first actual registration transaction did not reach its document lock", waiting)
		}
	}
	go func() { done <- f.service.EnsureConnectorVersions(ctx, b) }()
	if blocker != nil {
		deadline := time.Now().Add(8 * time.Second)
		var waiting int
		for waiting < 2 && time.Now().Before(deadline) {
			if _, err := blocker.ExecContext(ctx, "SELECT pg_stat_clear_snapshot()"); err != nil {
				t.Fatal(err)
			}
			if err := blocker.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE l.locktype='advisory' AND NOT l.granted AND a.application_name=$1`, f.appName).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting < 2 {
				time.Sleep(5 * time.Millisecond)
			}
		}
		if waiting != 2 {
			t.Fatal("two actual registration transactions did not reach their document locks", waiting)
		}
		rows, err := blocker.QueryContext(ctx, `SELECT l.pid,l.mode,l.granted,l.classid::bigint,l.objid::bigint FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE l.locktype='advisory' AND a.application_name=$1 ORDER BY l.pid,l.classid,l.objid,l.mode`, f.appName)
		if err != nil {
			t.Fatal(err)
		}
		locks := []map[string]any{}
		for rows.Next() {
			var pid int
			var mode string
			var granted bool
			var class, object int64
			if err = rows.Scan(&pid, &mode, &granted, &class, &object); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			locks = append(locks, map[string]any{"pid": pid, "mode": mode, "granted": granted, "classid": class, "objid": object})
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		raw, _ := json.Marshal(locks)
		t.Logf("actual PostgreSQL document blocker kind=%s id=%s key=%d waiting=%d locks=%s", kind, id, registrationLockKey(kind, id), waiting, raw)
		if err = blocker.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	errorsSeen := []string{}
	for i := 0; i < 2; i++ {
		err := <-done
		if err != nil {
			errorsSeen = append(errorsSeen, err.Error())
		} else {
			errorsSeen = append(errorsSeen, "success")
		}
	}
	t.Logf("two EnsureConnectorVersions results=%v", errorsSeen)
	for _, result := range errorsSeen {
		if result != "success" {
			t.Errorf("concurrent formal registration failed: %s", result)
		}
	}
}

func TestConnectorVersionRegistrationConcurrency(t *testing.T) {
	t.Run("same-existing-source", func(t *testing.T) {
		f := newRegistrationFixture(t)
		first := f.proof("shared", 1, 1)
		f.ensure(t, first.Reference)
		next := first
		next.NodeVersion = 2
		f.setProof(next)
		f.concurrent(t, "configuration_connector_source", model.ConnectorSecretReference(first.Reference.ID, 1), []model.ConfigurationReference{first.Reference}, []model.ConfigurationReference{first.Reference})
		if actual := f.source(t, first.Reference); actual.NodeVersion != 2 || actual.Reference != first.Reference {
			t.Fatal(actual)
		}
	})
	t.Run("same-first-registration", func(t *testing.T) {
		f := newRegistrationFixture(t)
		p := f.proof("new", 1, 1)
		f.concurrent(t, "connector_configuration_version", model.ConnectorSecretReference(p.Reference.ID, 1), []model.ConfigurationReference{p.Reference}, []model.ConfigurationReference{p.Reference})
		if actual := f.source(t, p.Reference); actual.Reference != p.Reference {
			t.Fatal(actual)
		}
	})
	t.Run("same-latest-upgrade", func(t *testing.T) {
		f := newRegistrationFixture(t)
		first := f.proof("upgrade", 1, 1)
		f.ensure(t, first.Reference)
		next := f.proof("upgrade", 2, 2)
		if _, err := f.service.Store.Put(context.Background(), "connector_configuration_version", model.ConnectorSecretReference(next.Reference.ID, 2), 0, next.Configuration); err != nil {
			t.Fatal(err)
		}
		f.concurrent(t, "connector_configuration", first.Reference.ID, []model.ConfigurationReference{next.Reference}, []model.ConfigurationReference{next.Reference})
		d, err := f.service.Store.Get(context.Background(), "connector_configuration", first.Reference.ID)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := store.Decode[model.ConnectorConfiguration](d)
		if err != nil || actual.Version != 2 {
			t.Fatal(actual, err)
		}
	})
	t.Run("reverse-overlapping-references", func(t *testing.T) {
		f := newRegistrationFixture(t)
		a, b := f.proof("order-a", 1, 1), f.proof("order-b", 1, 1)
		f.ensure(t, a.Reference, b.Reference)
		f.concurrent(t, "configuration_connector_source", model.ConnectorSecretReference(a.Reference.ID, 1), []model.ConfigurationReference{a.Reference, b.Reference}, []model.ConfigurationReference{b.Reference, a.Reference})
	})
	t.Run("same-connector-different-versions", func(t *testing.T) {
		f := newRegistrationFixture(t)
		a, b := f.proof("versions", 1, 1), f.proof("versions", 2, 2)
		f.ensure(t, a.Reference, b.Reference)
		f.concurrent(t, "configuration_connector_source", model.ConnectorSecretReference(a.Reference.ID, 1), []model.ConfigurationReference{a.Reference, b.Reference}, []model.ConfigurationReference{b.Reference})
		d, err := f.service.Store.Get(context.Background(), "connector_configuration", a.Reference.ID)
		if err != nil {
			t.Fatal(err)
		}
		c, err := store.Decode[model.ConnectorConfiguration](d)
		if err != nil || c.Version != 2 {
			t.Fatal(c, err)
		}
	})
}

func registrationSnapshot(t *testing.T, f *registrationFixture) string {
	t.Helper()
	rows, err := f.service.Store.DB.Query(`SELECT kind,id,version,data FROM documents WHERE kind IN ('connector_configuration','connector_configuration_version','configuration_connector_source') ORDER BY kind,id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := []any{}
	for rows.Next() {
		var kind, id, data string
		var version int64
		if err = rows.Scan(&kind, &id, &version, &data); err != nil {
			t.Fatal(err)
		}
		values = append(values, []any{kind, id, version, data})
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return store.Hash(values)
}

func TestConnectorVersionRegistrationAuthorityAndAtomicity(t *testing.T) {
	f := newRegistrationFixture(t)
	ctx := context.Background()
	original := f.proof("fixed", 1, 4)
	f.ensure(t, original.Reference)
	t.Run("old-fixed-read-keeps-latest", func(t *testing.T) {
		newer := f.proof("fixed", 2, 4)
		f.ensure(t, newer.Reference, original.Reference)
		d, err := f.service.Store.Get(ctx, "connector_configuration", original.Reference.ID)
		if err != nil {
			t.Fatal(err)
		}
		c, err := store.Decode[model.ConnectorConfiguration](d)
		if err != nil || c.Version != 2 {
			t.Fatal(c, err)
		}
	})
	t.Run("stale-node-version-rolls-back-earlier-reference", func(t *testing.T) {
		before := registrationSnapshot(t, f)
		fresh := f.proof("rollback-fresh", 1, 4)
		stale := original
		stale.NodeVersion = 3
		f.setProof(stale)
		if err := f.service.EnsureConnectorVersions(ctx, []model.ConfigurationReference{fresh.Reference, original.Reference}); !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
		if after := registrationSnapshot(t, f); after != before {
			t.Fatal("failed registration changed committed state")
		}
		f.setProof(original)
	})
	t.Run("immutable-fixed-digest-conflict", func(t *testing.T) {
		before := registrationSnapshot(t, f)
		wrong := original
		wrong.Configuration.UpdatedMS++
		wrong.Reference.Digest = ConnectorDigest(wrong.Configuration)
		f.setProof(wrong)
		if err := f.service.EnsureConnectorVersions(ctx, []model.ConfigurationReference{wrong.Reference}); !errors.Is(err, store.ErrConflict) {
			t.Fatal(err)
		}
		if registrationSnapshot(t, f) != before {
			t.Fatal("fixed conflict changed state")
		}
		f.setProof(original)
	})
	t.Run("wrong-node-or-source-proof", func(t *testing.T) {
		for _, change := range []func(*model.ConnectorRegistration){func(p *model.ConnectorRegistration) { p.NodeID = "edge-b" }, func(p *model.ConnectorRegistration) { p.SourceURL = "http://untrusted.invalid" }, func(p *model.ConnectorRegistration) { p.CertificateSHA256 = "wrong" }} {
			before := registrationSnapshot(t, f)
			wrong := original
			change(&wrong)
			f.setProof(wrong)
			if err := f.service.EnsureConnectorVersions(ctx, []model.ConfigurationReference{wrong.Reference}); !errors.Is(err, store.ErrConflict) {
				t.Fatal(err)
			}
			if registrationSnapshot(t, f) != before {
				t.Fatal("invalid registration changed state")
			}
		}
		f.setProof(original)
	})
	t.Run("authority-permission-revoked", func(t *testing.T) {
		before := registrationSnapshot(t, f)
		f.mu.Lock()
		f.status = http.StatusForbidden
		f.mu.Unlock()
		if err := f.service.EnsureConnectorVersions(ctx, []model.ConfigurationReference{original.Reference}); !errors.Is(err, identity.ErrDenied) {
			t.Fatal(err)
		}
		if registrationSnapshot(t, f) != before {
			t.Fatal("permission rejection changed state")
		}
		f.mu.Lock()
		f.status = http.StatusOK
		f.mu.Unlock()
	})
	t.Run("source-write-failure-atomic-rollback", func(t *testing.T) {
		fresh := f.proof("write-failure", 1, 5)
		before := registrationSnapshot(t, f)
		statement := `CREATE TRIGGER reject_registration_source BEFORE INSERT ON documents WHEN NEW.kind='configuration_connector_source' BEGIN SELECT RAISE(ABORT,'injected source persistence failure'); END`
		if f.admin != nil {
			statement = `CREATE FUNCTION reject_registration_source() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='configuration_connector_source' THEN RAISE EXCEPTION 'injected source persistence failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_registration_source BEFORE INSERT ON documents FOR EACH ROW EXECUTE FUNCTION reject_registration_source()`
		}
		if _, err := f.service.Store.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
		err := f.service.EnsureConnectorVersions(ctx, []model.ConfigurationReference{fresh.Reference})
		if err == nil || !strings.Contains(err.Error(), "injected source persistence failure") {
			t.Fatal(err)
		}
		if registrationSnapshot(t, f) != before {
			t.Fatal("failed source write committed public version or latest pointer")
		}
		if _, err = f.service.Store.DB.Exec(`DROP TRIGGER reject_registration_source` + map[bool]string{true: " ON documents", false: ""}[f.admin != nil]); err != nil {
			t.Fatal(err)
		}
		if f.admin != nil {
			if _, err = f.service.Store.DB.Exec(`DROP FUNCTION reject_registration_source()`); err != nil {
				t.Fatal(err)
			}
		}
		f.ensure(t, fresh.Reference)
	})
	t.Run("metadata-caller-fixed-reference", func(t *testing.T) {
		metadata, err := f.service.ConfigurationMetadata(ctx, []model.ConfigurationReference{original.Reference})
		if err != nil || len(metadata) != 1 || metadata[0].Reference != original.Reference || metadata[0].NodeIDs[0] != "edge-a" || metadata[0].CredentialRef != original.Configuration.CredentialRef {
			t.Fatal(metadata, err)
		}
	})
	t.Run("key-material-remains-public-only", func(t *testing.T) {
		for _, kind := range []string{"connector_configuration_version", "configuration_connector_source"} {
			documents, err := f.service.Store.List(ctx, kind)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range documents {
				if strings.Contains(string(d.Data), `"password"`) || strings.Contains(string(d.Data), `"payload"`) {
					t.Fatal("key material entered public registration")
				}
			}
		}
	})
}

// Keep input slices unmodified when callers provide reverse or repeated references.
func TestConnectorVersionRegistrationInputOrder(t *testing.T) {
	f := newRegistrationFixture(t)
	a, b := f.proof("input-a", 1, 1), f.proof("input-b", 1, 1)
	refs := []model.ConfigurationReference{b.Reference, a.Reference, b.Reference}
	before := store.Hash(refs)
	f.ensure(t, refs...)
	if store.Hash(refs) != before {
		t.Fatal("registration reordered the caller input")
	}
	sources, err := f.service.Store.List(context.Background(), "configuration_connector_source")
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	if len(sources) != 2 {
		t.Fatal(len(sources))
	}
}

func TestConnectorVersionRegistrationReaders(t *testing.T) {
	for _, mode := range []string{"latest-pointer-consumer", "reverse-fixed-consumer"} {
		t.Run(mode, func(t *testing.T) {
			f := newRegistrationFixture(t)
			if f.admin == nil {
				t.Skip("actual PostgreSQL lock scheduling required")
			}
			a, b := f.proof("reader", 1, 1), f.proof("reader", 2, 1)
			f.ensure(t, a.Reference, b.Reference)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			blocker, err := f.admin.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			if _, err = blocker.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", registrationLockKey("configuration_connector_source", model.ConnectorSecretReference(a.Reference.ID, a.Reference.Version))); err != nil {
				t.Fatal(err)
			}
			readerReady, readNext := make(chan struct{}), make(chan struct{})
			done := make(chan error, 2)
			w := model.WorkloadIdentity{NodeID: "edge-a", Program: "edge", Purpose: "runtime", ConnectorIDs: []string{a.Reference.ID}, Capabilities: []string{"connector:mqtt_device"}}
			go func() {
				done <- f.service.Store.Write(ctx, func(tx *store.Tx) error {
					if mode == "latest-pointer-consumer" {
						if _, e := tx.Read("connector_configuration", a.Reference.ID); e != nil {
							return e
						}
					} else {
						if _, e := f.service.envelopeTx(tx, w, b.Reference); e != nil {
							return e
						}
					}
					close(readerReady)
					select {
					case <-readNext:
					case <-ctx.Done():
						return ctx.Err()
					}
					ref := b.Reference
					if mode == "reverse-fixed-consumer" {
						ref = a.Reference
					}
					_, e := f.service.envelopeTx(tx, w, ref)
					return e
				})
			}()
			select {
			case <-readerReady:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			go func() {
				done <- f.service.EnsureConnectorVersions(ctx, []model.ConfigurationReference{a.Reference, b.Reference})
			}()
			deadline := time.Now().Add(8 * time.Second)
			var waiting int
			for waiting == 0 && time.Now().Before(deadline) {
				if _, err = blocker.ExecContext(ctx, "SELECT pg_stat_clear_snapshot()"); err != nil {
					t.Fatal(err)
				}
				if err = blocker.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE l.locktype='advisory' AND NOT l.granted AND a.application_name=$1`, f.appName).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting == 0 {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if waiting == 0 {
				t.Fatal("registration writer did not overlap the actual envelope consumer")
			}
			t.Logf("actual envelopeTx consumer=%s writer_waiting_locks=%d", mode, waiting)
			close(readNext)
			if err = blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err = <-done; err != nil {
					t.Errorf("registration or actual consumer failed: %v", err)
				}
			}
		})
	}
}

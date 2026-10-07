package coordination

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"competition2026/product/platform/internal/cloudsync"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"github.com/nats-io/nats.go"
)

// This fixture persists through independently started test processes. The
// orchestration script replaces one real server at a time between invocations.
// Database paths and credentials must belong to a disposable isolated site.
func TestPersistentThreeReplicaUpgrade(t *testing.T) {
	dir := os.Getenv("SF_SITE_MIGRATION_FIXTURE")
	if dir == "" {
		t.Skip("set SF_SITE_MIGRATION_FIXTURE to an isolated prepared .local/site directory")
	}
	step := os.Getenv("SF_SITE_MIGRATION_STEP")
	if step != "seed" && step != "verify" && step != "advance" {
		t.Fatal("SF_SITE_MIGRATION_STEP must be seed, verify, or advance")
	}
	var auth struct {
		Token string `json:"token"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &auth); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	prefix := "message_upgrade"
	executionID := "persisted-three-node-execution"
	database := filepath.Join(dir, "sqlite")
	if err = os.MkdirAll(database, 0700); err != nil {
		t.Fatal(err)
	}
	var stores []*store.Store
	var nodes []*Coordinator
	var configurations []*tls.Config
	for i, node := range []string{"edge-a", "edge-b", "edge-c"} {
		s, e := store.Open(ctx, filepath.Join(database, node+".db"), node, bytes.Repeat([]byte{byte(i + 1)}, 32))
		if e != nil {
			t.Fatal(e)
		}
		stores = append(stores, s)
		t.Cleanup(func() { _ = s.Close() })
		client, e := cloudsync.NewHTTPClient(filepath.Join(dir, "pki/ca.pem"), filepath.Join(dir, "pki", node+".pem"), filepath.Join(dir, "pki", node+".key"))
		if e != nil {
			t.Fatal(e)
		}
		configuration := client.Transport.(*http.Transport).TLSClientConfig
		configurations = append(configurations, configuration)
		c, e := Open(ctx, s, Options{URL: fmt.Sprintf("tls://127.0.0.1:%d", 14222+i), Token: auth.Token, Prefix: prefix, TLS: configuration, LeaseTTL: 2 * time.Second})
		if e != nil {
			t.Fatal(e)
		}
		nodes = append(nodes, c)
		t.Cleanup(c.Close)
	}
	for _, s := range stores {
		for _, peer := range stores {
			if _, err = s.Get(ctx, "entity", peer.NodeID); err == nil {
				continue
			}
			identity, _ := json.Marshal(map[string]string{"audit_public_key": base64.StdEncoding.EncodeToString(peer.SignKey.Public().(ed25519.PublicKey))})
			_, err = s.Put(ctx, "entity", peer.NodeID, 0, model.Entity{ID: peer.NodeID, Kind: "edge", Status: "active", Version: 1, Config: identity})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	statePath := filepath.Join(dir, "persistent-checkpoint.json")
	var expected model.Execution
	if step == "seed" {
		fence, release, e := nodes[0].Acquire(ctx, executionID, "edge-a", 2*time.Second)
		if e != nil {
			t.Fatal(e)
		}
		expected = model.Execution{DownlinkID: executionID, DefinitionID: "migration-strategy", DefinitionVersion: 1,
			CoordinatorID: "edge-a", Fence: fence, Status: "running", Actor: model.Actor{UserID: "migration-fixture", Source: "simulation"},
			Params: map[string]string{"migration": "old-server"}, Steps: []model.StepResult{{StepID: "first", CommandID: executionID + ":first", Status: "SUCCESS"}}}
		if e = nodes[0].Checkpoint(ctx, expected); e != nil {
			t.Fatal(e)
		}
		release()
		encoded, _ := json.MarshalIndent(expected, "", "  ")
		if e = os.WriteFile(statePath, encoded, 0600); e != nil {
			t.Fatal(e)
		}
	} else {
		encoded, e := os.ReadFile(statePath)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(encoded, &expected); e != nil {
			t.Fatal(e)
		}
	}
	for _, c := range nodes {
		recovered, e := c.Recover(ctx, executionID)
		if e != nil || !reflect.DeepEqual(recovered, expected) {
			t.Fatalf("checkpoint was changed or failed signature verification on %s: %v %+v", c.Store.NodeID, e, recovered)
		}
	}
	checks := map[string]bool{"signed_checkpoint_preserved_on_all_nodes": true}
	if step == "advance" {
		old := expected
		fence, release, e := nodes[1].Acquire(ctx, executionID, "edge-b", 2*time.Second)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
		if fence <= old.Fence {
			t.Fatalf("fence did not advance: %d -> %d", old.Fence, fence)
		}
		if e = nodes[2].Validate(ctx, executionID, old.CoordinatorID, old.Fence); e == nil {
			t.Fatal("old execution owner accepted after takeover")
		}
		if e = nodes[0].Checkpoint(ctx, old); e == nil {
			t.Fatal("stale coordinator rewrote the checkpoint")
		}
		expected.CoordinatorID, expected.Fence = "edge-b", fence
		expected.Steps = append(expected.Steps, model.StepResult{StepID: "second", CommandID: executionID + ":second", Status: "SUCCESS"})
		if e = nodes[1].Checkpoint(ctx, expected); e != nil {
			t.Fatal(e)
		}
		encoded, _ := json.MarshalIndent(expected, "", "  ")
		if e = os.WriteFile(statePath, encoded, 0600); e != nil {
			t.Fatal(e)
		}
		checks["new_fence_greater_than_old"] = true
		checks["stale_owner_and_checkpoint_rejected"] = true
		for _, c := range nodes {
			recovered, e := c.Recover(ctx, executionID)
			if e != nil || !reflect.DeepEqual(recovered, expected) {
				t.Fatalf("new checkpoint was not visible on %s: %v", c.Store.NodeID, e)
			}
		}
	}
	// Flush forces authentication errors to surface even when the server sends
	// its TLS-required INFO before verifying the client certificate.
	for name, configuration := range map[string]*tls.Config{
		"missing_client_certificate":  func() *tls.Config { c := configurations[0].Clone(); c.Certificates = nil; return c }(),
		"wrong_certificate_authority": func() *tls.Config { c := configurations[0].Clone(); c.RootCAs = x509.NewCertPool(); return c }(),
		"wrong_authorization_token":   configurations[0].Clone(),
	} {
		token := auth.Token
		if name == "wrong_authorization_token" {
			token = "isolated-invalid-token"
		}
		connection, e := nats.Connect("tls://127.0.0.1:14222", nats.Token(token), nats.Secure(configuration), nats.Timeout(time.Second), nats.NoReconnect())
		if e == nil {
			e = connection.FlushTimeout(time.Second)
			if e == nil {
				e = connection.LastError()
			}
			connection.Close()
		}
		if e == nil {
			t.Fatal("invalid authenticated connection accepted:", name)
		}
		checks[name+"_rejected"] = true
	}
	var streams []*nats.StreamInfo
	deadline := time.Now().Add(40 * time.Second)
	for {
		streams = nil
		ready := true
		for _, suffix := range []string{"lease", "journal", "active"} {
			info, e := nodes[0].JS.StreamInfo("KV_"+prefix+"_"+suffix, nats.Context(ctx))
			if e != nil {
				t.Fatal(e)
			}
			streams = append(streams, info)
			if info.Config.Replicas != 3 || info.Cluster == nil || len(info.Cluster.Replicas) != 2 || info.Cluster.Leader == "" {
				ready = false
				continue
			}
			for _, peer := range info.Cluster.Replicas {
				if !peer.Current || peer.Offline || peer.Lag != 0 {
					ready = false
				}
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("three replicas were not current after replacement: %+v", streams)
		}
		time.Sleep(250 * time.Millisecond)
	}
	checks["three_file_replicas_current"] = true
	checkpoint, err := nodes[0].get(ctx, nodes[0].Journal, key(executionID))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(checkpoint.Value)
	report := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "step": step, "checks": checks,
		"execution": expected, "checkpoint_revision": checkpoint.Revision, "checkpoint_sha256": hex.EncodeToString(digest[:]), "streams": streams}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(dir, "migration-result.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("persistent checkpoint verified: owner=%s fence=%d revision=%d digest=%x; three file replicas current", expected.CoordinatorID, expected.Fence, checkpoint.Revision, digest)
}

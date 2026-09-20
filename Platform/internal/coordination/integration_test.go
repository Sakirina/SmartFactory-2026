package coordination

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nats-io/nats.go"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/cloudsync"
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type deviceRecorder struct {
	mu       sync.Mutex
	commands map[string]int
}

func (d *deviceRecorder) Send(ctx context.Context, step model.Step, id string, deadline int64) (control.DispatchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commands[id]++
	return control.DispatchResult{Status: "SUCCESS", Message: "simulated device applied"}, nil
}
func (d *deviceRecorder) count(id string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.commands[id]
}

type siteFixture struct {
	nodes       []*Coordinator
	stores      []*store.Store
	services    []*control.Service
	devices     []*deviceRecorder
	definition  model.Definition
	ctx         context.Context
	cancel      context.CancelFunc
	commandDone []chan error
	root        string
}

func realSite(t *testing.T) *siteFixture {
	t.Helper()
	dir := os.Getenv("SF_SITE_INTEGRATION")
	if dir == "" {
		t.Skip("set SF_SITE_INTEGRATION to the prepared .local/site directory")
	}
	dir, e := filepath.Abs(dir)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if e != nil {
		t.Fatal(e)
	}
	var auth struct {
		Token string `json:"token"`
	}
	if e = json.Unmarshal(raw, &auth); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &siteFixture{ctx: ctx, cancel: cancel, root: filepath.Dir(filepath.Dir(dir))}
	prefix := fmt.Sprintf("test_%d", time.Now().UnixNano())
	for i, node := range []string{"edge-a", "edge-b", "edge-c"} {
		s, e := store.Open(ctx, filepath.Join(t.TempDir(), node+".db"), node, bytes.Repeat([]byte{byte(i + 1)}, 32))
		if e != nil {
			t.Fatal(e)
		}
		f.stores = append(f.stores, s)
		client, e := cloudsync.NewHTTPClient(filepath.Join(dir, "pki/ca.pem"), filepath.Join(dir, "pki", node+".pem"), filepath.Join(dir, "pki", node+".key"))
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 && os.Getenv("SF_SITE_RESET_TEST_BUCKETS") == "1" {
			nc, err := nats.Connect("tls://127.0.0.1:14222", nats.Token(auth.Token), nats.Secure(client.Transport.(*http.Transport).TLSClientConfig))
			if err != nil {
				t.Fatal(err)
			}
			js, err := nc.JetStream()
			if err != nil {
				t.Fatal(err)
			}
			for name := range js.StreamNames() {
				if strings.HasPrefix(name, "KV_test_") {
					if err = js.DeleteStream(name); err != nil {
						t.Fatal(err)
					}
				}
			}
			nc.Close()
		}
		c, e := Open(ctx, s, Options{URL: "tls://127.0.0.1:14222,tls://127.0.0.1:14223,tls://127.0.0.1:14224", Token: auth.Token, Prefix: prefix, TLS: client.Transport.(*http.Transport).TLSClientConfig, LeaseTTL: 2 * time.Second})
		if e != nil {
			t.Fatal(e)
		}
		f.nodes = append(f.nodes, c)
	}
	f.definition = model.Definition{ID: "gas-plan", Kind: "strategy", Name: "Site gas response", Status: "published", Version: 1, Policy: model.Policy{EdgeIDs: []string{"edge-a", "edge-b", "edge-c"}, RiskCategory: "business", RiskLevel: 1}}
	for _, node := range f.nodes {
		f.definition.Policy.Steps = append(f.definition.Policy.Steps, model.Step{ID: "extract-" + node.Store.NodeID, DeviceID: "device-" + node.Store.NodeID, EdgeID: node.Store.NodeID, Action: "extract", Idempotent: true, TimeoutMS: 2000})
		f.definition.Policy.Degraded = append(f.definition.Policy.Degraded, model.Step{ID: "stop-" + node.Store.NodeID, DeviceID: "device-" + node.Store.NodeID, EdgeID: node.Store.NodeID, Action: "stop", Idempotent: true, TimeoutMS: 2000})
	}
	for i, c := range f.nodes {
		for _, peer := range f.nodes {
			identity, _ := json.Marshal(map[string]string{"audit_public_key": base64.StdEncoding.EncodeToString(peer.Store.SignKey.Public().(ed25519.PublicKey))})
			for _, entity := range []model.Entity{{ID: peer.Store.NodeID, Kind: "edge", Status: "active", Version: 1, Config: identity}, {ID: "device-" + peer.Store.NodeID, Kind: "device", EdgeID: peer.Store.NodeID, Status: "approved", Version: 1}} {
				if _, e = c.Store.Put(ctx, "entity", entity.ID, 0, entity); e != nil {
					t.Fatal(e)
				}
			}
		}
		if _, e = c.Store.Put(ctx, "definition", f.definition.ID, 0, f.definition); e != nil {
			t.Fatal(e)
		}
		devices := &deviceRecorder{commands: map[string]int{}}
		f.devices = append(f.devices, devices)
		service := &control.Service{Store: c.Store, Definitions: &engine.Service{Store: c.Store}, NodeID: c.Store.NodeID, Edge: true, Coordinator: c, Dispatcher: &Dispatcher{Coordinator: c, Local: devices}}
		f.services = append(f.services, service)
		done := make(chan error, 1)
		f.commandDone = append(f.commandDone, done)
		go func() { done <- c.ServeCommands(ctx, service) }()
		if e = c.PublishState(ctx); e != nil {
			t.Fatal(e)
		}
		_ = i
	}
	t.Cleanup(func() {
		cancel()
		for i, c := range f.nodes {
			if i == 1 {
				for _, suffix := range []string{"state", "lease", "journal", "active"} {
					deadline := time.Now().Add(15 * time.Second)
					for {
						err := c.JS.DeleteKeyValue(prefix + "_" + suffix)
						if err == nil || errors.Is(err, nats.ErrStreamNotFound) {
							break
						}
						if time.Now().After(deadline) {
							t.Error("test bucket cleanup", err)
							break
						}
						time.Sleep(200 * time.Millisecond)
					}
				}
			}
			c.Close()
		}
		for _, done := range f.commandDone {
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Error("site command service did not stop")
			}
		}
		for _, s := range f.stores {
			s.Close()
		}
	})
	for _, c := range f.nodes {
		if e = c.Ready(ctx, f.definition); e != nil {
			t.Fatal(e)
		}
	}
	return f
}
func execution(f *siteFixture, id string) model.Execution {
	return model.Execution{DownlinkID: id, DefinitionID: f.definition.ID, DefinitionVersion: 1, Status: "queued", StartDeadlineMS: time.Now().Add(10 * time.Second).UnixMilli(), Actor: model.Actor{UserID: "published-policy", Source: "simulation"}, Params: map[string]string{}}
}
func TestRealThreeReplicaCoordination(t *testing.T) {
	f := realSite(t)
	ctx := f.ctx
	t.Run("single_owner_and_fencing", func(t *testing.T) {
		fence, release, e := f.nodes[0].Acquire(ctx, "lease", "edge-a", 2*time.Second)
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = f.nodes[1].Acquire(ctx, "lease", "edge-b", 2*time.Second); !errors.Is(e, control.ErrLeaseHeld) {
			t.Fatal("concurrent owner accepted", e)
		}
		release()
		next, releaseNext, e := f.nodes[1].Acquire(ctx, "lease", "edge-b", 2*time.Second)
		if e != nil {
			t.Fatal(e)
		}
		defer releaseNext()
		if next <= fence {
			t.Fatal("fence did not advance")
		}
		if e = f.nodes[2].Validate(ctx, "lease", "edge-a", fence); e == nil {
			t.Fatal("old execution owner accepted")
		}
	})
	t.Run("all_devices_owned_locally_and_deduplicated", func(t *testing.T) {
		req := execution(f, "distributed")
		result, e := f.services[0].Run(ctx, req, true)
		if e != nil || result.Status != "completed" {
			t.Fatal(e, result.Status, result.Reason)
		}
		if _, e = f.services[1].Run(ctx, req, true); e != nil {
			t.Fatal(e)
		}
		for i, step := range f.definition.Policy.Steps {
			if f.devices[i].count(req.DownlinkID+":"+step.ID) != 1 {
				t.Fatal("duplicated or missing physical action", step.ID)
			}
		}
	})
	t.Run("version_agreement", func(t *testing.T) {
		entry, e := f.nodes[2].get(ctx, f.nodes[2].State, key("edge-c"))
		if e != nil {
			t.Fatal(e)
		}
		var state NodeState
		if _, e = f.nodes[2].unpack(ctx, entry.Value, &state); e != nil {
			t.Fatal(e)
		}
		delete(state.Definitions, "gas-plan:1")
		raw, _ := f.nodes[2].pack(state)
		if _, e = f.nodes[2].State.Put(key("edge-c"), raw); e != nil {
			t.Fatal(e)
		}
		if e = f.nodes[0].Ready(ctx, f.definition); e == nil {
			t.Fatal("policy version mismatch accepted")
		}
		if e = f.nodes[2].PublishState(ctx); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("coordinator_process_loss_and_progress_recovery", func(t *testing.T) {
		req := execution(f, "recover")
		req.Status = "running"
		req.CoordinatorID = "edge-a"
		fence, _, e := f.nodes[0].Acquire(ctx, req.DownlinkID, "edge-a", time.Second)
		if e != nil {
			t.Fatal(e)
		}
		req.Fence = fence
		first := f.definition.Policy.Steps[0]
		id := req.DownlinkID + ":" + first.ID
		if _, e = f.devices[0].Send(ctx, first, id, time.Now().Add(time.Second).UnixMilli()); e != nil {
			t.Fatal(e)
		}
		req.Steps = []model.StepResult{{StepID: first.ID, CommandID: id, Status: "SUCCESS"}}
		if e = f.nodes[0].Checkpoint(ctx, req); e != nil {
			t.Fatal(e)
		}
		f.nodes[0].Close()
		timer := time.NewTimer(1200 * time.Millisecond)
		<-timer.C
		result, e := f.services[1].Run(ctx, req, true)
		if e != nil || result.Status != "completed" {
			t.Fatal(e, result.Status, result.Reason)
		}
		if result.CoordinatorID != "edge-b" || result.Fence <= fence {
			t.Fatal("coordinator did not change", result.CoordinatorID)
		}
		for i, step := range f.definition.Policy.Steps {
			if f.devices[i].count(req.DownlinkID+":"+step.ID) != 1 {
				t.Fatal("recovery duplicated or omitted a completed action", step.ID)
			}
		}
	})
	if os.Getenv("SF_SITE_FAULTS") == "1" {
		t.Run("quorum_loss_uses_only_local_degraded_actions", func(t *testing.T) {
			compose := filepath.Join(f.root, "deploy/compose.site.json")
			command := func(args ...string) {
				t.Helper()
				all := append([]string{"compose", "-f", compose}, args...)
				if out, e := exec.CommandContext(context.Background(), "docker", all...).CombinedOutput(); e != nil {
					t.Fatalf("docker fault injection: %s %v", out, e)
				}
			}
			command("stop", "-t", "1", "nats-2", "nats-3")
			defer command("start", "nats-2", "nats-3")
			req := execution(f, "partition")
			req.StartDeadlineMS = time.Now().Add(time.Minute).UnixMilli()
			result, e := f.services[1].Run(ctx, req, true)
			if e != nil || result.Status != "degraded_completed" {
				t.Fatal(e, result.Status, result.Reason)
			}
			if f.devices[1].count("partition:stop-edge-b") != 1 || f.devices[0].count("partition:stop-edge-a") != 0 || f.devices[2].count("partition:stop-edge-c") != 0 {
				t.Fatal("degraded actions escaped their local owner")
			}
		})
	}
}

// Package coordination coordinates site-local execution through a three-replica
// JetStream quorum. Physical writes remain in the target edge's durable journal.
package coordination

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"github.com/nats-io/nats.go"
)

type Options struct {
	URL, Token, Prefix string
	TLS                *tls.Config
	LeaseTTL           time.Duration
}
type Coordinator struct {
	Store                          *store.Store
	Connection                     *nats.Conn
	JS                             nats.JetStreamContext
	State, Leases, Journal, Active nats.KeyValue
	Prefix                         string
	LeaseTTL                       time.Duration
	mu                             sync.Mutex
	held                           map[string]context.CancelFunc
}
type signed struct {
	NodeID    string          `json:"node_id"`
	Payload   json.RawMessage `json:"payload"`
	Signature string          `json:"signature"`
}
type lease struct {
	ExecutionID string `json:"execution_id"`
	Owner       string `json:"owner"`
	Fence       uint64 `json:"fence"`
	ExpiresMS   int64  `json:"expires_ms"`
}
type entry struct {
	Revision uint64
	Value    []byte
}
type NodeState struct {
	NodeID       string              `json:"node_id"`
	AtMS         int64               `json:"at_ms"`
	Definitions  map[string]string   `json:"definitions"`
	Observations []store.IngestBatch `json:"observations"`
}

func Open(ctx context.Context, s *store.Store, o Options) (*Coordinator, error) {
	if o.Prefix == "" {
		o.Prefix = "smartfactory"
	}
	for _, ch := range o.Prefix {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
			return nil, errors.New("coordination prefix must be alphanumeric")
		}
	}
	if o.TLS == nil || o.Token == "" {
		return nil, errors.New("NATS coordination requires mutual TLS and authorization")
	}
	if o.LeaseTTL <= 0 {
		o.LeaseTTL = 15 * time.Second
	}
	if o.LeaseTTL < time.Second {
		return nil, errors.New("coordination lease must last at least one second")
	}
	connection, e := nats.Connect(o.URL, nats.Name("sf-"+s.NodeID), nats.Token(o.Token), nats.Secure(o.TLS), nats.Timeout(3*time.Second), nats.MaxReconnects(-1), nats.ReconnectWait(250*time.Millisecond), nats.PingInterval(time.Second), nats.MaxPingsOutstanding(2))
	if e != nil {
		return nil, e
	}
	js, e := connection.JetStream(nats.MaxWait(2 * time.Second))
	if e != nil {
		connection.Close()
		return nil, e
	}
	c := &Coordinator{Store: s, Connection: connection, JS: js, Prefix: o.Prefix, LeaseTTL: o.LeaseTTL, held: map[string]context.CancelFunc{}}
	for _, spec := range []struct {
		suffix string
		ttl    time.Duration
		target *nats.KeyValue
	}{{"state", 30 * time.Second, &c.State}, {"lease", 0, &c.Leases}, {"journal", 0, &c.Journal}, {"active", 0, &c.Active}} {
		name := o.Prefix + "_" + spec.suffix
		kv, e := js.KeyValue(name)
		if errors.Is(e, nats.ErrBucketNotFound) {
			kv, e = js.CreateKeyValue(&nats.KeyValueConfig{Bucket: name, Replicas: 3, History: 1, TTL: spec.ttl, Storage: nats.FileStorage, MaxValueSize: 2 << 20, MaxBytes: 512 << 20})
		}
		if e != nil {
			connection.Close()
			return nil, e
		}
		info, e := js.StreamInfo("KV_"+name, nats.Context(ctx))
		if e != nil {
			connection.Close()
			return nil, e
		}
		if info.Config.Replicas != 3 {
			connection.Close()
			return nil, errors.New("coordination requires exactly three replicas")
		}
		*spec.target = kv
	}
	return c, nil
}
func key(id string) string { sum := sha256.Sum256([]byte(id)); return hex.EncodeToString(sum[:]) }
func (c *Coordinator) pack(v any) ([]byte, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	x := signed{NodeID: c.Store.NodeID, Payload: raw}
	x.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(c.Store.SignKey, append([]byte("sf-coordination-v1:"+x.NodeID+":"), raw...)))
	return json.Marshal(x)
}
func (c *Coordinator) unpack(ctx context.Context, raw []byte, v any) (string, error) {
	var x signed
	if e := store.DecodeJSON(raw, &x); e != nil {
		return "", e
	}
	var public ed25519.PublicKey
	if x.NodeID == c.Store.NodeID {
		public = c.Store.SignKey.Public().(ed25519.PublicKey)
	} else {
		doc, e := c.Store.Get(ctx, "entity", x.NodeID)
		if e != nil {
			return "", e
		}
		entity, e := store.Decode[model.Entity](doc)
		if e != nil {
			return "", e
		}
		if entity.Kind != "edge" || entity.Status != "active" {
			return "", errors.New("site node is not admitted")
		}
		var identity struct {
			Public string `json:"audit_public_key"`
		}
		if e = store.DecodeJSON(entity.Config, &identity); e != nil {
			return "", e
		}
		public, e = base64.StdEncoding.DecodeString(identity.Public)
		if e != nil {
			return "", e
		}
	}
	signature, e := base64.StdEncoding.DecodeString(x.Signature)
	if e != nil || len(public) != ed25519.PublicKeySize || !ed25519.Verify(public, append([]byte("sf-coordination-v1:"+x.NodeID+":"), x.Payload...), signature) {
		return "", errors.New("untrusted site message signature")
	}
	return x.NodeID, store.DecodeJSON(x.Payload, v)
}

// GetLastMsg uses the stream leader API; a subsequent CAS obtains a majority
// acknowledgement before any lease can authorize a physical action.
func (c *Coordinator) get(ctx context.Context, bucket nats.KeyValue, k string) (entry, error) {
	message, e := c.JS.GetLastMsg("KV_"+bucket.Bucket(), "$KV."+bucket.Bucket()+"."+k, nats.Context(ctx))
	if errors.Is(e, nats.ErrMsgNotFound) {
		return entry{}, store.ErrNotFound
	}
	if e != nil {
		return entry{}, e
	}
	if operation := message.Header.Get("KV-Operation"); operation == "DEL" || operation == "PURGE" {
		return entry{}, store.ErrNotFound
	}
	return entry{Revision: message.Sequence, Value: message.Data}, nil
}
func (c *Coordinator) lease(ctx context.Context, id string) (lease, entry, error) {
	e, err := c.get(ctx, c.Leases, key(id))
	if err != nil {
		return lease{}, e, err
	}
	var current lease
	owner, err := c.unpack(ctx, e.Value, &current)
	if err != nil {
		return current, e, err
	}
	if owner != current.Owner || current.ExecutionID != id {
		return current, e, errors.New("lease identity mismatch")
	}
	return current, e, nil
}
func (c *Coordinator) Acquire(ctx context.Context, id, owner string, ttl time.Duration) (uint64, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if owner != c.Store.NodeID {
		return 0, nil, errors.New("cannot acquire another node's lease")
	}
	if ttl <= 0 || ttl > c.LeaseTTL {
		ttl = c.LeaseTTL
	}
	current, previous, e := c.lease(ctx, id)
	if e == nil && current.ExpiresMS > c.Store.Now().UnixMilli() {
		return 0, nil, control.ErrLeaseHeld
	}
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		return 0, nil, e
	}
	candidate := lease{ExecutionID: id, Owner: owner, ExpiresMS: c.Store.Now().Add(ttl).UnixMilli()}
	raw, e := c.pack(candidate)
	if e != nil {
		return 0, nil, e
	}
	var revision uint64
	if previous.Revision == 0 {
		revision, e = c.Leases.Create(key(id), raw)
	} else {
		revision, e = c.Leases.Update(key(id), raw, previous.Revision)
	}
	if e != nil {
		if errors.Is(e, nats.ErrKeyExists) {
			return 0, nil, control.ErrLeaseHeld
		}
		return 0, nil, e
	}
	candidate.Fence = revision
	raw, e = c.pack(candidate)
	if e != nil {
		return 0, nil, e
	}
	if _, e = c.Leases.Update(key(id), raw, revision); e != nil {
		// A concurrent participant may confirm the current lease revision
		// between the initial claim and its fence commit. Contention is not
		// a site outage and must never authorize a degraded physical action.
		if errors.Is(e, nats.ErrKeyExists) {
			return 0, nil, control.ErrLeaseHeld
		}
		return 0, nil, e
	}
	renew, cancel := context.WithCancel(ctx)
	c.held[id] = cancel
	go func() {
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-renew.Done():
				return
			case <-ticker.C:
				if e := c.renew(renew, id, revision, ttl); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	release := func() {
		cancel()
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.held, id)
		call, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		value, old, e := c.lease(call, id)
		if e != nil || value.Fence != revision || value.Owner != owner {
			return
		}
		value.ExpiresMS = 0
		raw, e := c.pack(value)
		if e == nil {
			_, _ = c.Leases.Update(key(id), raw, old.Revision)
		}
	}
	return revision, release, nil
}
func (c *Coordinator) renew(ctx context.Context, id string, fence uint64, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 3; attempt++ {
		current, previous, e := c.lease(ctx, id)
		if e != nil {
			return e
		}
		if current.Owner != c.Store.NodeID || current.Fence != fence || current.ExpiresMS <= c.Store.Now().UnixMilli() {
			return errors.New("execution lease expired")
		}
		current.ExpiresMS = c.Store.Now().Add(ttl).UnixMilli()
		raw, e := c.pack(current)
		if e != nil {
			return e
		}
		_, e = c.Leases.Update(key(id), raw, previous.Revision)
		if !errors.Is(e, nats.ErrKeyExists) {
			return e
		}
	}
	return errors.New("execution lease was concurrently updated")
}
func (c *Coordinator) Validate(ctx context.Context, id, owner string, fence uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 3; attempt++ {
		value, entry, e := c.lease(ctx, id)
		if e != nil {
			return e
		}
		if value.Owner != owner || value.Fence != fence || fence == 0 || value.ExpiresMS <= c.Store.Now().UnixMilli() {
			return errors.New("execution fence is stale or expired")
		}
		if _, e = c.Leases.Update(key(id), entry.Value, entry.Revision); e == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return errors.New("execution lease could not be confirmed by the quorum")
}
func (c *Coordinator) Ready(ctx context.Context, d model.Definition) error {
	for _, node := range d.Policy.EdgeIDs {
		entry, e := c.get(ctx, c.State, key(node))
		if e != nil {
			return fmt.Errorf("node %s is unavailable: %w", node, e)
		}
		var state NodeState
		signer, e := c.unpack(ctx, entry.Value, &state)
		if e != nil {
			return e
		}
		age := c.Store.Now().UnixMilli() - state.AtMS
		if signer != node || state.NodeID != node || age < -5000 || age > c.Store.Policy().OfflineMS {
			return fmt.Errorf("node %s state is stale", node)
		}
		if state.Definitions[fmt.Sprintf("%s:%d", d.ID, d.Version)] != store.Hash(d) {
			return fmt.Errorf("node %s has not applied the same policy version", node)
		}
	}
	// This write requires the three-replica stream to retain its majority.
	raw, e := c.pack(map[string]any{"node": c.Store.NodeID, "at_ms": c.Store.Now().UnixMilli()})
	if e != nil {
		return e
	}
	_, e = c.Active.Put("probe_"+key(c.Store.NodeID), raw)
	return e
}
func (c *Coordinator) Recover(ctx context.Context, id string) (model.Execution, error) {
	entry, e := c.get(ctx, c.Journal, key(id))
	if e != nil {
		return model.Execution{}, e
	}
	var execution model.Execution
	node, e := c.unpack(ctx, entry.Value, &execution)
	if e != nil {
		return execution, e
	}
	if execution.DownlinkID != id || execution.CoordinatorID != node {
		return execution, errors.New("execution journal identity mismatch")
	}
	return execution, nil
}
func isTerminal(state string) bool {
	return state == "completed" || state == "degraded_completed" || state == "rejected" || state == "failed" || state == "result_unknown"
}
func (c *Coordinator) Checkpoint(ctx context.Context, execution model.Execution) error {
	if execution.CoordinatorID != c.Store.NodeID {
		return errors.New("only the current coordinator can checkpoint")
	}
	if e := c.Validate(ctx, execution.DownlinkID, execution.CoordinatorID, execution.Fence); e != nil {
		return e
	}
	raw, e := c.pack(execution)
	if e != nil {
		return e
	}
	if !isTerminal(execution.Status) {
		if _, e = c.Active.Put(key(execution.DownlinkID), raw); e != nil {
			return e
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		old, err := c.get(ctx, c.Journal, key(execution.DownlinkID))
		if errors.Is(err, store.ErrNotFound) {
			_, e = c.Journal.Create(key(execution.DownlinkID), raw)
		} else if err != nil {
			return err
		} else {
			var prior model.Execution
			if _, e = c.unpack(ctx, old.Value, &prior); e != nil {
				return e
			}
			if prior.Fence > execution.Fence {
				return errors.New("older coordinator attempted to replace journal")
			}
			if prior.Fence == execution.Fence && isTerminal(prior.Status) && store.Hash(prior) != store.Hash(execution) {
				return store.ErrConflict
			}
			_, e = c.Journal.Update(key(execution.DownlinkID), raw, old.Revision)
		}
		if e == nil {
			if isTerminal(execution.Status) {
				return c.Active.Delete(key(execution.DownlinkID))
			}
			return nil
		}
	}
	return e
}
func (c *Coordinator) Pending(ctx context.Context) ([]model.Execution, error) {
	keys, e := c.Active.Keys()
	if errors.Is(e, nats.ErrNoKeysFound) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	result := []model.Execution{}
	for _, id := range keys {
		if strings.HasPrefix(id, "probe_") {
			continue
		}
		entry, e := c.get(ctx, c.Active, id)
		if errors.Is(e, store.ErrNotFound) {
			continue
		}
		if e != nil {
			return nil, e
		}
		var execution model.Execution
		if _, e = c.unpack(ctx, entry.Value, &execution); e != nil {
			return nil, e
		}
		if isTerminal(execution.Status) {
			continue
		}
		lease, _, e := c.lease(ctx, execution.DownlinkID)
		if e == nil && lease.ExpiresMS > c.Store.Now().UnixMilli() {
			continue
		}
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			return nil, e
		}
		result = append(result, execution)
	}
	return result, nil
}
func (c *Coordinator) Close() {
	c.mu.Lock()
	for _, cancel := range c.held {
		cancel()
	}
	c.mu.Unlock()
	c.Connection.Close()
}

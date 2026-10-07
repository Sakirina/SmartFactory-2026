package configcenter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type RuntimeConfiguration struct {
	Reference       model.ConfigurationReference `json:"reference"`
	EffectiveValue  any                          `json:"effective_value,omitempty"`
	ApplyGeneration int64                        `json:"apply_generation,omitempty"`
	ConsumerDigest  string                       `json:"consumer_digest,omitempty"`
}

type workloadHTTPStatusError struct {
	Status int
	Path   string
}

func (e *workloadHTTPStatusError) Error() string {
	return fmt.Sprintf("workload operation %s returned %d", e.Path, e.Status)
}

func rejectedWorkload(err error) bool {
	var status *workloadHTTPStatusError
	return errors.As(err, &status) && (status.Status == http.StatusUnauthorized || status.Status == http.StatusForbidden)
}

func (s *Service) rememberRuntime(id string, value any) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeValues == nil {
		s.runtimeValues = map[string]any{}
	}
	s.runtimeValues[id] = value
}

func (s *Service) RuntimeValue(ctx context.Context, id string) (any, error) {
	p := s.Store.Policy()
	values := map[string]any{"storage.auto_backfill_days": p.AutoBackfillDays, "heartbeat.interval_ms": p.HeartbeatMS, "heartbeat.offline_ms": p.OfflineMS, "control.approval_ttl_ms": p.ApprovalTTLMS, "control.start_ttl_ms": p.StartTTLMS, "identity.offline_ttl_ms": p.PermissionTTLMS, "ui.refresh_ms": p.RefreshMS, "queue.capacity": p.QueueCapacity, "storage.retention": p.Retention, "storage.archive": p.Archive, "control.confirmations": p.Confirmations, "queue.watermarks": map[string]int64{"reminder": p.Reminder, "warning": p.Warning, "error": p.Error, "recovery": p.Recovery}}
	if value, ok := values[id]; ok {
		return value, nil
	}
	s.runtimeMu.RLock()
	value, ok := s.runtimeValues[id]
	s.runtimeMu.RUnlock()
	if !ok {
		return nil, store.ErrNotFound
	}
	return value, nil
}

func (s *Service) RuntimeSnapshot(ctx context.Context) ([]RuntimeConfiguration, error) {
	docs, err := s.Store.List(ctx, "configuration_runtime")
	if err != nil {
		return nil, err
	}
	out := []RuntimeConfiguration{}
	for _, d := range docs {
		var e model.ConfigurationEnvelope
		if err = store.DecodeJSON(d.Data, &e); err != nil {
			return nil, err
		}
		r := RuntimeConfiguration{Reference: e.Reference}
		if e.Reference.Kind == "connector" && s.ReadConnectorRuntime != nil {
			r.ApplyGeneration, r.ConsumerDigest, err = s.ReadConnectorRuntime(ctx, e.Reference)
			if err != nil {
				return nil, err
			}
		}
		if e.Reference.Kind == "parameter" && e.CredentialRef == "" {
			r.EffectiveValue, err = s.RuntimeValue(ctx, e.Reference.ID)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func (c *Subscriber) internalCall(ctx context.Context, method, path string, body, out any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.WorkloadToken)
	req.Header.Set("X-SF-Instance-ID", c.InstanceID)
	req.Header.Set("Content-Type", "application/json")
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := c.client().Do(req.WithContext(call))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1024))
		return &workloadHTTPStatusError{Status: res.StatusCode, Path: path}
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<20))
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 4<<20))
	decoder.UseNumber()
	return decoder.Decode(out)
}

func (c *Subscriber) Activate(ctx context.Context) error {
	if c.InstanceID == "" {
		c.InstanceID = fmt.Sprintf("process-%d", time.Now().UnixNano())
	}
	if err := c.internalCall(ctx, "POST", "/internal/workloads/session", map[string]string{"instance_id": c.InstanceID}, &c.Workload); err != nil {
		return err
	}
	if c.Workload.NodeID != c.NodeID {
		return errors.New("authenticated node differs from local node")
	}
	return nil
}

func (c *Subscriber) RunWorkload(ctx context.Context) error {
	activated := false
	for {
		var err error
		if !activated {
			err = c.Activate(ctx)
			if err == nil {
				activated = true
			} else if !rejectedWorkload(err) {
				// Startup keeps the last validated local configuration while authority
				// or the config service is unavailable. New static values wait for restart.
				if cached, e := c.Local.Store.List(ctx, "configuration_runtime"); e == nil && len(cached) > 0 {
					c.started = true
					for _, d := range cached {
						var env model.ConfigurationEnvelope
						if store.DecodeJSON(d.Data, &env) == nil && env.Reference.Kind == "parameter" {
							if v, e := c.Local.Value(ctx, env.Reference.ID); e == nil {
								c.Local.rememberRuntime(env.Reference.ID, v)
							}
						}
					}
				}
			}
		}
		if activated {
			err = c.listenWorkload(ctx)
		}
		if ctx.Err() != nil {
			return nil
		}
		if rejectedWorkload(err) {
			// A stream may be between connections when its identity is revoked.
			// Explicit rejection clears managed values just like identity_changed;
			// network/authority failures keep the validated cache.
			if pruneErr := c.pruneManaged(ctx, nil); pruneErr != nil {
				err = pruneErr
			}
		}
		if c.Local != nil {
			_ = c.Local.Store.Write(ctx, func(tx *store.Tx) error {
				return tx.SetEphemeral("configuration_connection", c.NodeID, map[string]any{"status": "retrying", "at_ms": c.Local.Store.Now().UnixMilli()})
			})
		}
		_ = err
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

func (c *Subscriber) listenWorkload(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.URL, "/")+"/internal/config/v2/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.WorkloadToken)
	req.Header.Set("X-SF-Instance-ID", c.InstanceID)
	res, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return &workloadHTTPStatusError{Status: res.StatusCode, Path: "/internal/config/v2/stream"}
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var last []model.ConfigurationEnvelope
	beats := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: identity_changed") {
			if err := c.pruneManaged(ctx, nil); err != nil {
				return err
			}
			return errors.New("workload identity changed")
		}
		if strings.HasPrefix(line, ": heartbeat") {
			beats++
			if beats%5 == 0 && len(last) > 0 {
				if err = c.ApplyEnvelopes(ctx, last, nil, false); err != nil {
					return err
				}
			}
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var env []model.ConfigurationEnvelope
		if err = store.DecodeJSON([]byte(strings.TrimPrefix(line, "data: ")), &env); err != nil {
			return err
		}
		if err = c.pruneManaged(ctx, env); err != nil {
			return err
		}
		// Metadata may change while credentials remain valid; refresh the revision
		// without replacing the active process instance or its epoch.
		if len(env) > 0 {
			c.Workload.Program = env[0].Program
			c.Workload.Purpose = env[0].Purpose
		}
		if err = c.ApplyEnvelopes(ctx, env, nil, !c.started); err != nil {
			return err
		}
		last = env
		c.started = true
		if err = c.Local.Store.Write(ctx, func(tx *store.Tx) error {
			return tx.SetEphemeral("configuration_connection", c.NodeID, map[string]any{"status": "connected", "identity_id": c.Workload.ID, "instance_id": c.InstanceID, "at_ms": c.Local.Store.Now().UnixMilli()})
		}); err != nil {
			return err
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	return errors.New("configuration stream ended")
}

func (c *Subscriber) pruneManaged(ctx context.Context, envelopes []model.ConfigurationEnvelope) error {
	allowed := map[string]bool{}
	for _, e := range envelopes {
		allowed[e.Reference.Kind+":"+e.Reference.ID] = true
	}
	docs, err := c.Local.Store.List(ctx, "configuration_runtime")
	if err != nil {
		return err
	}
	changed := false
	for _, d := range docs {
		if allowed[d.ID] {
			continue
		}
		var old model.ConfigurationEnvelope
		if err = store.DecodeJSON(d.Data, &old); err != nil {
			return err
		}
		if old.Reference.Kind == "connector" && c.Local.RemoveConnectorRuntime != nil {
			if err = c.Local.RemoveConnectorRuntime(ctx, old.Reference); err != nil {
				return err
			}
		}
		if err = c.Local.Store.Write(ctx, func(tx *store.Tx) error {
			if old.Reference.Kind == "parameter" {
				if e := tx.Delete("parameter", old.Reference.ID); e != nil {
					return e
				}
			}
			return tx.Delete("configuration_runtime", d.ID)
		}); err != nil {
			return err
		}
		c.Local.runtimeMu.Lock()
		delete(c.Local.runtimeValues, old.Reference.ID)
		c.Local.runtimeMu.Unlock()
		changed = true
	}
	if changed {
		return c.Local.ApplyPolicy(ctx)
	}
	return nil
}

func (c *Subscriber) payload(ctx context.Context, e model.ConfigurationEnvelope, private map[string]json.RawMessage) (any, json.RawMessage, error) {
	if e.CredentialRef == "" {
		return e.Value, nil, nil
	}
	raw := private[e.CredentialRef]
	if len(raw) == 0 {
		var out model.CredentialPayload
		err := c.internalCall(ctx, "POST", "/internal/config/v2/credentials/resolve", model.CredentialResolution{Reference: e.Reference, CredentialRef: e.CredentialRef, Purpose: e.Purpose}, &out)
		if err != nil {
			return nil, nil, err
		}
		if out.Reference != e.Reference || out.CredentialRef != e.CredentialRef {
			return nil, nil, store.ErrConflict
		}
		raw = out.Payload
	}
	var v any
	if e.Reference.Kind == "parameter" {
		if err := store.DecodeJSON(raw, &v); err != nil {
			return nil, nil, err
		}
	}
	return v, raw, nil
}

func (c *Subscriber) report(ctx context.Context, e model.ConfigurationEnvelope, state, reason string, running model.ConfigurationVersion, effective any) error {
	if c.WorkloadToken == "" {
		return nil
	}
	if c.sequences == nil {
		c.sequences = map[string]int64{}
	}
	key := e.Reference.Kind + ":" + e.Reference.ID
	// Pending retries retain their sequence and body until acknowledged.
	c.sequences[key]++
	want := model.ConfigurationVersion{Version: e.Reference.Version, Digest: e.Reference.Digest}
	r := model.ConfigurationReport{IdentityID: c.Workload.ID, NodeID: c.NodeID, Program: c.Workload.Program, Purpose: c.Workload.Purpose, InstanceID: c.InstanceID, InstanceEpoch: c.Workload.InstanceEpoch, Generation: c.Workload.Generation, Sequence: c.sequences[key], Kind: e.Reference.Kind, ID: e.Reference.ID, Desired: want, Prepared: want, Applied: running, Running: running, State: state, Reason: reason}
	if e.CredentialRef == "" {
		r.EffectiveValue = effective
	}
	var out model.ConfigurationReport
	return c.internalCall(ctx, "POST", "/internal/config/v2/reports", r, &out)
}

func (c *Subscriber) ApplyEnvelopes(ctx context.Context, envelopes []model.ConfigurationEnvelope, private map[string]json.RawMessage, startup bool) error {
	for _, e := range envelopes {
		if e.NodeID != c.NodeID || c.Workload.Program != "" && (e.Program != c.Workload.Program || e.Purpose != c.Workload.Purpose) {
			return errors.New("configuration target differs from authenticated program")
		}
		key := e.Reference.Kind + ":" + e.Reference.ID
		var prior model.ConfigurationEnvelope
		priorDoc, priorErr := c.Local.Store.Get(ctx, "configuration_runtime", key)
		if priorErr == nil {
			if err := store.DecodeJSON(priorDoc.Data, &prior); err != nil {
				return err
			}
		} else if !errors.Is(priorErr, store.ErrNotFound) {
			return priorErr
		}
		running := model.ConfigurationVersion{Version: prior.Reference.Version, Digest: prior.Reference.Digest}
		if prior.Reference.Version > e.Reference.Version && !startup {
			return store.ErrConflict
		}
		if d, err := c.Local.Store.Get(ctx, "configuration_received", fmt.Sprintf("%s:%d", key, e.Reference.Version)); err == nil {
			var old model.ConfigurationReference
			if err = store.DecodeJSON(d.Data, &old); err != nil {
				return err
			}
			if old != e.Reference {
				return store.ErrConflict
			}
		} else if errors.Is(err, store.ErrNotFound) {
			if err = c.Local.Store.Write(ctx, func(tx *store.Tx) error {
				return tx.SetEphemeral("configuration_received", fmt.Sprintf("%s:%d", key, e.Reference.Version), e.Reference)
			}); err != nil {
				return err
			}
		} else {
			return err
		}
		if prior.Reference == e.Reference && !startup {
			var effective any
			if e.Reference.Kind == "parameter" && e.CredentialRef == "" {
				var err error
				effective, err = c.Local.RuntimeValue(ctx, e.Reference.ID)
				if err != nil {
					return err
				}
			}
			if err := c.report(ctx, e, "running", "", running, effective); err != nil {
				return err
			}
			continue
		}
		if err := c.Local.Store.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral("configuration_prepared", key, e) }); err != nil {
			return err
		}
		if !e.Dynamic && !startup {
			if err := c.report(ctx, e, "restart_required", "restart_required", running, nil); err != nil {
				return err
			}
			continue
		}
		value, raw, err := c.payload(ctx, e, private)
		if err != nil {
			return err
		}
		var old store.Document
		oldErr := store.ErrNotFound
		if e.Reference.Kind == "parameter" {
			if err = Validate(e.Schema, value); err != nil {
				return errors.New("configuration schema mismatch")
			}
			p := Parameter{ID: e.Reference.ID, Program: e.Program, Schema: e.Schema, Value: value, Dynamic: e.Dynamic, Secret: e.CredentialRef != "", CredentialRef: e.CredentialRef, Version: e.Reference.Version, ContentDigest: e.Reference.Digest}
			if err = validateKnown(p); err != nil {
				return errors.New("configuration semantic validation failed")
			}
			old, oldErr = c.Local.Store.Get(ctx, "parameter", p.ID)
			if oldErr != nil && !errors.Is(oldErr, store.ErrNotFound) {
				return oldErr
			}
			if p.Secret {
				bytes, e2 := json.Marshal(value)
				if e2 != nil {
					return e2
				}
				cipher, e2 := c.Local.Identity.Encrypt("config:"+p.ID, string(bytes))
				if e2 != nil {
					return e2
				}
				p.Value = map[string]any{"ciphertext": cipher}
			}
			if err = c.Local.Store.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral("parameter", p.ID, p) }); err != nil {
				return err
			}
			if c.OnApply != nil {
				err = c.OnApply(ctx)
			} else {
				err = c.Local.ApplyPolicy(ctx)
			}
			if err != nil {
				if rollback := c.Local.Store.Write(ctx, func(tx *store.Tx) error {
					if oldErr == nil {
						return tx.SetEphemeral("parameter", p.ID, old.Data)
					}
					return tx.Delete("parameter", p.ID)
				}); rollback != nil {
					return rollback
				}
				// Reinstall the previous policy after a consumer fails partway through.
				if rollback := c.Local.ApplyPolicy(ctx); rollback != nil {
					return rollback
				}
				if err = c.report(ctx, e, "failed", "runtime_apply_failed", running, nil); err != nil {
					return err
				}
				continue
			}
			c.Local.rememberRuntime(p.ID, value)
			if c.ReadEffective != nil {
				value, err = c.ReadEffective(ctx, e)
			} else {
				value, err = c.Local.RuntimeValue(ctx, p.ID)
			}
			if err != nil {
				return err
			}
			if e.CredentialRef == "" && store.Hash(value) != store.Hash(e.Value) {
				return errors.New("consumer effective configuration differs from prepared value")
			}
		} else {
			if e.Connector == nil || e.Connector.EdgeID != c.NodeID {
				return errors.New("connector payload target mismatch")
			}
			if c.ApplyConnector == nil {
				return errors.New("connector consumer is unavailable")
			}
			if len(raw) == 0 {
				raw = e.Connector.Config
			}
			err = c.ApplyConnector(ctx, *e.Connector, raw)
			if err != nil {
				if err = c.report(ctx, e, "failed", "connector_apply_failed", running, nil); err != nil {
					return err
				}
				continue
			}
		}
		if err = c.Local.Store.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral("configuration_runtime", key, e) }); err != nil {
			return err
		}
		running = model.ConfigurationVersion{Version: e.Reference.Version, Digest: e.Reference.Digest}
		if err = c.report(ctx, e, "running", "", running, value); err != nil {
			return err
		}
	}
	return nil
}

// ApplyReleaseConfiguration is a startup application of fixed, authorized payloads.
// Secret payloads remain private and are encrypted in the receiving local store.
func (s *Service) ApplyReleaseConfiguration(ctx context.Context, envelopes []model.ConfigurationEnvelope, private map[string]json.RawMessage) error {
	c := Subscriber{NodeID: s.Store.NodeID, Local: s, OnApply: s.ApplyPolicy}
	return c.ApplyEnvelopes(ctx, envelopes, private, true)
}

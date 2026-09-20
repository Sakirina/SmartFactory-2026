package plugins

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Configuration interface {
	Value(context.Context, string) (any, error)
}
type Spec struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Kind             string         `json:"kind"`
	Executable       string         `json:"executable"`
	Args             []string       `json:"args"`
	PollMS           int64          `json:"poll_ms"`
	Enabled          bool           `json:"enabled"`
	CredentialID     string         `json:"credential_id,omitempty"`
	PushCredentialID string         `json:"push_credential_id,omitempty"`
	Config           map[string]any `json:"config"`
	Version          int64          `json:"version"`
}
type Status struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	LastMS   int64  `json:"last_ms"`
	NextMS   int64  `json:"next_ms"`
	Sequence int64  `json:"sequence"`
	Error    string `json:"error,omitempty"`
}
type PullRequest struct {
	Method      string         `json:"method"`
	Source      string         `json:"source"`
	After       int64          `json:"after"`
	Config      map[string]any `json:"config"`
	Credentials any            `json:"credentials,omitempty"`
}
type Manager struct {
	Store  *store.Store
	Config Configuration
}

func Validate(spec Spec) error {
	if spec.ID == "" || strings.ContainsAny(spec.ID, "/\\\r\n") || spec.Name == "" || spec.Kind != "organization" {
		return errors.New("organization plugin id and name required")
	}
	if !filepath.IsAbs(spec.Executable) || spec.PollMS < 1000 || spec.PollMS > 86400000 || len(spec.Args) > 64 {
		return errors.New("absolute executable and poll interval from 1 second to 1 day required")
	}
	for _, arg := range spec.Args {
		if len(arg) > 4096 {
			return errors.New("plugin argument exceeds limit")
		}
	}
	return nil
}
func (m *Manager) Put(ctx context.Context, actor model.Actor, spec Spec, expected int64) (Spec, error) {
	if err := Validate(spec); err != nil {
		return spec, err
	}
	if expected < 0 {
		return spec, store.ErrConflict
	}
	spec.Version = expected + 1
	err := m.Store.Write(ctx, func(tx *store.Tx) error {
		if _, e := tx.Put("plugin", spec.ID, expected, spec); e != nil {
			return e
		}
		return tx.Audit(actor, "plugin.configure", spec.ID, "", spec)
	})
	return spec, err
}
func (m *Manager) Poll(ctx context.Context) error {
	docs, err := m.Store.List(ctx, "plugin")
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	errorsOut := make(chan error, len(docs))
	slots := make(chan struct{}, 4)
	for _, doc := range docs {
		spec, e := store.Decode[Spec](doc)
		if e != nil {
			return e
		}
		if !spec.Enabled {
			continue
		}
		var state Status
		if saved, e := m.Store.Get(ctx, "plugin_status", spec.ID); e == nil {
			state, e = store.Decode[Status](saved)
			if e != nil {
				return e
			}
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if state.NextMS > m.Store.Now().UnixMilli() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			if e := m.pull(ctx, spec); e != nil {
				errorsOut <- e
			}
		}()
	}
	wg.Wait()
	close(errorsOut)
	all := []error{}
	for e := range errorsOut {
		all = append(all, e)
	}
	return errors.Join(all...)
}

type boundedOutput struct {
	bytes.Buffer
	Max int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.Max {
		return 0, errors.New("plugin output exceeds limit")
	}
	return b.Buffer.Write(p)
}
func (m *Manager) pull(ctx context.Context, spec Spec) error {
	input := PullRequest{Method: "pull", Source: spec.ID, Config: spec.Config}
	if doc, e := m.Store.Get(ctx, "organization_cursor", spec.ID); e == nil {
		var v struct {
			Sequence int64 `json:"sequence"`
		}
		if e = store.DecodeJSON(doc.Data, &v); e != nil {
			return e
		}
		input.After = v.Sequence
	}
	state := Status{ID: spec.ID, State: "healthy", Sequence: input.After, LastMS: m.Store.Now().UnixMilli()}
	state.NextMS = state.LastMS + spec.PollMS
	err := func() error {
		if spec.CredentialID != "" {
			if m.Config == nil {
				return errors.New("plugin credentials unavailable")
			}
			value, e := m.Config.Value(ctx, spec.CredentialID)
			if e != nil {
				return e
			}
			input.Credentials = value
		}
		raw, e := json.Marshal(input)
		if e != nil {
			return e
		}
		call, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		command := exec.CommandContext(call, spec.Executable, spec.Args...)
		command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
		command.Stdin = bytes.NewReader(raw)
		output := &boundedOutput{Max: 2 << 20}
		command.Stdout = output
		command.Stderr = io.Discard
		if e = command.Run(); e != nil {
			return fmt.Errorf("plugin process failed: %T", e)
		}
		var batch OrganizationSync
		if e = store.DecodeJSON(output.Bytes(), &batch); e != nil {
			return errors.New("plugin returned invalid organization JSON")
		}
		if batch.Source != spec.ID {
			return errors.New("plugin source identity mismatch")
		}
		if batch.Sequence <= input.After {
			return nil
		}
		if e = SyncOrganization(ctx, m.Store, model.Actor{UserID: "plugin:" + spec.ID, Source: "plugin-pull"}, batch); e != nil {
			return e
		}
		state.Sequence = batch.Sequence
		return nil
	}()
	if err != nil {
		state.State = "failed"
		state.Error = err.Error()
	}
	if e := m.Store.Write(ctx, func(tx *store.Tx) error { return tx.SetEphemeral("plugin_status", spec.ID, state) }); e != nil {
		return e
	}
	return err
}
func (m *Manager) Push(w http.ResponseWriter, r *http.Request) {
	specDoc, err := m.Store.Get(r.Context(), "plugin", r.PathValue("id"))
	if err != nil {
		http.Error(w, "unknown plugin", 404)
		return
	}
	spec, err := store.Decode[Spec](specDoc)
	if err != nil || !spec.Enabled || spec.PushCredentialID == "" || m.Config == nil {
		http.Error(w, "plugin push unavailable", 403)
		return
	}
	value, err := m.Config.Value(r.Context(), spec.PushCredentialID)
	if err != nil {
		http.Error(w, "plugin credentials unavailable", 503)
		return
	}
	var credential struct {
		Token string `json:"token"`
	}
	raw, _ := json.Marshal(value)
	_ = json.Unmarshal(raw, &credential)
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if credential.Token == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(credential.Token)) != 1 {
		http.Error(w, "authentication required", 401)
		return
	}
	raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		http.Error(w, "invalid push body", 400)
		return
	}
	var batch OrganizationSync
	if err = store.DecodeJSON(raw, &batch); err != nil || batch.Source != spec.ID {
		http.Error(w, "invalid organization source", 400)
		return
	}
	if err = SyncOrganization(r.Context(), m.Store, model.Actor{UserID: "plugin:" + spec.ID, Source: "plugin-push"}, batch); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"committed": true, "id": batch.ID, "sequence": batch.Sequence})
}

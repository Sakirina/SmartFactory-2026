// Package releaseruntime applies a verified, fixed release before the ordinary
// application begins serving or starts any workers.
package releaseruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"competition2026/product/platform/internal/compiledplan"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/buildinfo"
	"competition2026/product/platform/pkg/model"
)

type Staged struct {
	NodeID               string                        `json:"node_id"`
	Release              model.Release                 `json:"release"`
	Configurations       []model.ConfigurationEnvelope `json:"configurations"`
	Private              map[string]json.RawMessage    `json:"private,omitempty"`
	CredentialGeneration int64                         `json:"credential_generation,omitempty"`
}
type Cached struct {
	Format        string `json:"format"`
	NodeID        string `json:"node_id"`
	ReleaseSHA256 string `json:"release_sha256"`
	Ciphertext    string `json:"ciphertext"`
}

func Seal(staged Staged, cipher *identity.Manager) (Cached, error) {
	c := Cached{Format: "smartfactory-private-release-v1", NodeID: staged.NodeID, ReleaseSHA256: staged.Release.SHA256}
	raw, err := json.Marshal(staged)
	if err != nil {
		return c, err
	}
	c.Ciphertext, err = cipher.Encrypt("release-cache:"+c.NodeID+":"+c.ReleaseSHA256, string(raw))
	return c, err
}
func Unseal(c Cached, nodeID string, cipher *identity.Manager) (Staged, error) {
	var staged Staged
	if c.Format != "smartfactory-private-release-v1" || c.NodeID != nodeID || !releasebundle.ValidDigest(c.ReleaseSHA256) {
		return staged, errors.New("private release cache identity or format differs")
	}
	raw, err := cipher.Decrypt("release-cache:"+c.NodeID+":"+c.ReleaseSHA256, c.Ciphertext)
	if err != nil {
		return staged, errors.New("private release cache could not be authenticated")
	}
	if err = store.DecodeJSON([]byte(raw), &staged); err != nil {
		return staged, err
	}
	if staged.NodeID != c.NodeID || staged.Release.SHA256 != c.ReleaseSHA256 {
		return staged, errors.New("private release cache content identity differs")
	}
	return staged, nil
}

type Activation struct {
	ReleaseSHA256     string `json:"release_sha256"`
	SourceSHA256      string `json:"source_sha256"`
	AppliedSHA256     string `json:"applied_sha256"`
	DefinitionVersion int64  `json:"definition_version"`
}
type Runtime struct {
	mu            sync.Mutex
	Store         *store.Store
	Config        *configcenter.Service
	Staged        Staged
	InstanceID    string
	StartedMS     int64
	ProgramSHA256 string
	Build         model.ProgramBuild
}

func Open(ctx context.Context, s *store.Store, cfg *configcenter.Service, program, nodeID, path string, connector func(context.Context, model.ConnectorConfiguration, json.RawMessage) error) (*Runtime, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8<<20 {
		return nil, errors.New("release payload requires a private regular file within 8 MiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cached Cached
	if err = store.DecodeJSON(raw, &cached); err != nil {
		return nil, err
	}
	staged, err := Unseal(cached, nodeID, cfg.Identity)
	if err != nil {
		return nil, err
	}
	if staged.NodeID != nodeID || staged.Release.Manifest.Program != program || staged.Release.SHA256 != releasebundle.ManifestDigest(staged.Release.Manifest) {
		return nil, errors.New("staged release target or digest differs")
	}
	v := releasebundle.Validate(ctx, staged.Release.Manifest)
	if !v.Valid {
		return nil, fmt.Errorf("staged release is invalid: %v", v.Issues)
	}
	sha, err := buildinfo.ExecutableSHA256()
	if err != nil {
		return nil, err
	}
	build := buildinfo.Current(program)
	component := releasebundle.Program(staged.Release.Manifest)
	if sha != component.SHA256 || store.Hash(build) != store.Hash(component.Build) {
		return nil, errors.New("running executable differs from the release artifact or build metadata")
	}
	migrations, err := s.MigrationRecords(ctx)
	if err != nil {
		return nil, err
	}
	if len(migrations) == 0 {
		return nil, errors.New("database has no migration metadata")
	}
	if err = releasebundle.CheckDatabase(build, migrations[len(migrations)-1].Version); err != nil {
		return nil, err
	}
	refs := releasebundle.ReferencesForNode(staged.Release.Manifest, nodeID)
	if len(refs) != len(staged.Configurations) {
		return nil, errors.New("staged configuration set differs from the release")
	}
	for i, ref := range refs {
		if staged.Configurations[i].Reference != ref || staged.Configurations[i].NodeID != nodeID {
			return nil, errors.New("staged configuration reference or node differs")
		}
	}
	if err = cfg.PinLocalRelease(ctx, staged.Configurations); err != nil {
		return nil, err
	}
	consumer := configcenter.Subscriber{NodeID: nodeID, Local: cfg, OnApply: cfg.ApplyPolicy, ApplyConnector: connector}
	if err = consumer.ApplyEnvelopes(ctx, staged.Configurations, staged.Private, true); err != nil {
		return nil, err
	}
	// Each activation is a new local definition revision, including a supported
	// rollback. Source version/digest remains in the activation record so rule
	// history and data generated before the rollback keep their original identity.
	err = s.Write(ctx, func(tx *store.Tx) error {
		activeSHA256 := ""
		if current, e := tx.Get("release_runtime", "active"); e == nil {
			var active struct {
				SHA256 string `json:"sha256"`
			}
			if e = store.DecodeJSON(current.Data, &active); e != nil {
				return e
			}
			activeSHA256 = active.SHA256
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		retained := map[string]bool{}
		for _, c := range releasebundle.ComponentsForNode(staged.Release.Manifest, nodeID) {
			if c.Kind == "rule" {
				retained[c.ID] = true
			}
		}
		previous, e := tx.List("release_rule_activation")
		if e != nil {
			return e
		}
		for _, item := range previous {
			if retained[item.ID] {
				continue
			}
			doc, e := tx.Get("definition", item.ID)
			if errors.Is(e, store.ErrNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			d, e := store.Decode[model.Definition](doc)
			if e != nil {
				return e
			}
			if d.Status != "published" {
				continue
			}
			d.Status = "disabled"
			d.Version = doc.Version + 1
			d.EffectiveMS = s.CurrentTime().UnixMilli()
			if _, e = tx.Put("definition", d.ID, doc.Version, d); e != nil {
				return e
			}
			if e = tx.Audit(model.Actor{UserID: "release-agent", Source: "release-runtime"}, "release.rule.disable", d.GroupID, d.ID, map[string]any{"release_sha256": staged.Release.SHA256, "version": d.Version}); e != nil {
				return e
			}
		}
		for _, id := range staged.Release.Order {
			var c model.ReleaseComponent
			for _, item := range releasebundle.ComponentsForNode(staged.Release.Manifest, nodeID) {
				if item.ID == id {
					c = item
					break
				}
			}
			if c.Kind != "rule" {
				continue
			}
			activationDoc, e := tx.Get("release_rule_activation", id)
			if e == nil {
				a, e := store.Decode[Activation](activationDoc)
				if e != nil {
					return e
				}
				if activeSHA256 == staged.Release.SHA256 && a.ReleaseSHA256 == staged.Release.SHA256 && a.SourceSHA256 == c.SHA256 {
					current, e := tx.Get("definition", id)
					if e != nil {
						return e
					}
					var d model.Definition
					if e = store.DecodeJSON(current.Data, &d); e != nil {
						return e
					}
					if store.Hash(d) != a.AppliedSHA256 {
						return errors.New("active rule differs from its release activation")
					}
					continue
				}
			} else if !errors.Is(e, store.ErrNotFound) {
				return e
			}
			current, e := tx.Get("definition", id)
			if e != nil && !errors.Is(e, store.ErrNotFound) {
				return e
			}
			var d model.Definition
			if e = store.DecodeJSON(c.Content, &d); e != nil {
				return e
			}
			d.Version = current.Version + 1
			d.EffectiveMS = s.CurrentTime().UnixMilli()
			d.Status = "published"
			d.ExecutionPlan = nil
			plan, e := compiledplan.Compile(ctx, d)
			if e != nil {
				return e
			}
			d.ExecutionPlan = plan
			if e = compiledplan.Put(tx, d, plan); e != nil {
				return e
			}
			if _, e = tx.Put("definition", id, current.Version, d); e != nil {
				return e
			}
			a := Activation{ReleaseSHA256: staged.Release.SHA256, SourceSHA256: c.SHA256, AppliedSHA256: store.Hash(d), DefinitionVersion: d.Version}
			if _, e = tx.Put("release_rule_activation", id, activationDoc.Version, a); e != nil {
				return e
			}
			if e = tx.Audit(model.Actor{UserID: "release-agent", Source: "release-runtime"}, "release.rule.activate", d.GroupID, id, a); e != nil {
				return e
			}
		}
		return tx.SetEphemeral("release_runtime", "active", map[string]any{"release_id": staged.Release.ID, "sha256": staged.Release.SHA256, "program_sha256": sha})
	})
	if err != nil {
		return nil, err
	}
	r := &Runtime{Store: s, Config: cfg, Staged: staged, InstanceID: identity.ID(), StartedMS: s.CurrentTime().UnixMilli(), ProgramSHA256: sha, Build: build}
	if _, err = r.Snapshot(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Runtime) Snapshot(ctx context.Context) (model.ReleaseRuntime, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := model.ReleaseRuntime{NodeID: r.Store.NodeID, Program: r.Build.Program, ProcessInstanceID: r.InstanceID, PID: os.Getpid(), StartedMS: r.StartedMS, ProgramSHA256: r.ProgramSHA256, Build: r.Build, ReleaseID: r.Staged.Release.ID, ReleaseSHA256: r.Staged.Release.SHA256, Components: []model.ReleaseRuntimeComponent{}, PolicySHA256: store.Hash(r.Store.Policy()), Healthy: true}
	migrations, err := r.Store.MigrationRecords(ctx)
	if err != nil {
		return result, err
	}
	if len(migrations) > 0 {
		result.MigrationVersion = migrations[len(migrations)-1].Version
	}
	configurations, err := r.Config.RuntimeSnapshot(ctx)
	if err != nil {
		return result, err
	}
	for _, c := range releasebundle.ComponentsForNode(r.Staged.Release.Manifest, r.Store.NodeID) {
		actual := model.ReleaseRuntimeComponent{ID: c.ID, Kind: c.Kind, Version: c.Version, SHA256: c.SHA256, AppliedSHA256: c.SHA256}
		switch c.Kind {
		case "program":
			if c.SHA256 != r.ProgramSHA256 {
				return result, errors.New("program digest changed")
			}
		case "rule":
			doc, e := r.Store.Get(ctx, "definition", c.ID)
			if e != nil {
				return result, e
			}
			d, e := store.Decode[model.Definition](doc)
			if e != nil {
				return result, e
			}
			aDoc, e := r.Store.Get(ctx, "release_rule_activation", c.ID)
			if e != nil {
				return result, e
			}
			a, e := store.Decode[Activation](aDoc)
			if e != nil {
				return result, e
			}
			if a.ReleaseSHA256 != result.ReleaseSHA256 || a.SourceSHA256 != c.SHA256 || a.AppliedSHA256 != store.Hash(d) {
				return result, errors.New("running rule differs from fixed release")
			}
			if _, e = compiledplan.Load(ctx, r.Store, d); e != nil {
				return result, e
			}
			actual.AppliedSHA256 = a.AppliedSHA256
			actual.AppliedVersion = d.Version
		case "configuration":
			found := false
			for _, runtime := range configurations {
				if runtime.Reference == *c.Configuration {
					for _, envelope := range r.Staged.Configurations {
						if envelope.Reference == runtime.Reference && envelope.Reference.Kind == "parameter" && envelope.CredentialRef == "" && store.Hash(runtime.EffectiveValue) != store.Hash(envelope.Value) {
							return result, errors.New("effective configuration value differs from the release")
						}
					}
					found = true
					actual.AppliedSHA256 = runtime.Reference.Digest
					actual.AppliedVersion = runtime.Reference.Version
				}
			}
			if !found {
				return result, fmt.Errorf("configuration %s is not running at the fixed release version", c.ID)
			}
		}
		result.Components = append(result.Components, actual)
	}
	return result, nil
}

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/internal/scenetemplates"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type TemplateEvolutionTarget struct {
	InstanceID      string         `json:"instance_id" required:"true"`
	TemplateVersion int64          `json:"template_version" required:"true" minimum:"1"`
	Parameters      map[string]any `json:"parameters,omitempty"`
}
type EvolveTemplateBatchInput struct {
	ID              string                         `json:"id" required:"true"`
	RequestID       string                         `json:"request_id" required:"true"`
	ExpectedVersion int64                          `json:"expected_version" required:"true" minimum:"1"`
	ReleaseID       string                         `json:"release_id" required:"true"`
	Name            string                         `json:"name" required:"true"`
	ProgramSHA256   string                         `json:"program_sha256" required:"true"`
	Targets         []TemplateEvolutionTarget      `json:"targets" required:"true" minItems:"1" maxItems:"20"`
	Configurations  []model.ConfigurationReference `json:"configurations,omitempty"`
}

func (s *Releases) EvolveTemplateBatch(ctx context.Context, p identity.Principal, id string, in EvolveTemplateBatchInput) (model.TemplateEvolution, error) {
	var out model.TemplateEvolution
	if s.Mode != "cloud" {
		return out, errors.New("template evolution requires the cloud coordinator")
	}
	for _, value := range []string{id, in.ID, in.RequestID, in.ReleaseID} {
		if err := businessID(value); err != nil {
			return out, err
		}
	}
	batch, err := s.Business.TemplateBatch(ctx, p, id)
	if err != nil {
		return out, err
	}
	seen, err := s.Business.authorize(ctx, p, "publish", []string{batch.GroupID})
	if err != nil {
		return out, err
	}
	if err = seen.remember("template_batch", id, batch.Version); err != nil {
		return out, err
	}
	hash := store.Hash(in)
	scope := "template-evolve:" + id + ":" + p.User.ID
	if old, e := s.Store.Get(ctx, "template_evolution", in.ID); e == nil {
		out, e = store.Decode[model.TemplateEvolution](old)
		if e != nil {
			return out, e
		}
		if out.CreatedBy != p.User.ID || out.SourceBatchID != id || out.RequestSHA256 != hash {
			return model.TemplateEvolution{}, store.ErrConflict
		}
		if _, e = s.Get(ctx, p, out.Release.ID); e != nil {
			return model.TemplateEvolution{}, e
		}
		return out, nil
	} else if !errors.Is(e, store.ErrNotFound) {
		return out, e
	}
	if batch.Status != "prepared" || batch.Version != in.ExpectedVersion {
		return out, store.ErrConflict
	}
	if len(in.Targets) == 0 || len(in.Targets) > len(batch.Input.Instances) {
		return out, errors.New("select existing template instances to evolve")
	}
	artifact, err := releasebundle.Artifact(ctx, s.Store, in.ProgramSHA256)
	if err != nil {
		return out, err
	}
	program := model.ReleaseComponent{ID: "program", Kind: "program", Version: artifact.Build.Version, SHA256: artifact.SHA256, Format: releasebundle.ProgramFormat, Build: &artifact.Build}
	manifest := model.ReleaseManifest{SchemaVersion: releasebundle.Format, ID: in.ReleaseID, Name: in.Name, Program: artifact.Build.Program, Components: []model.ReleaseComponent{program}}
	out = model.TemplateEvolution{ID: in.ID, SourceBatchID: id, SourceBatchVersion: batch.Version, Instances: []model.TemplateInstance{}, Bindings: []model.TemplateBinding{}, DeviceIDs: []string{}, NodeIDs: []string{}, DefinitionIDs: []string{}, CreatedBy: p.User.ID, CreatedMS: s.Store.CurrentTime().UnixMilli(), RequestSHA256: hash}
	selected := map[string]bool{}
	configurationIDs := map[string]bool{}
	for _, target := range in.Targets {
		if selected[target.InstanceID] {
			return out, errors.New("template evolution repeats an instance")
		}
		selected[target.InstanceID] = true
		var instance model.TemplateInstance
		var binding model.TemplateBinding
		for _, v := range batch.Input.Instances {
			if v.ID == target.InstanceID {
				instance = v
				break
			}
		}
		for _, v := range batch.Bindings {
			if v.InstanceID == target.InstanceID {
				binding = v
				break
			}
		}
		if instance.ID == "" || binding.InstanceID == "" {
			return out, errors.New("template evolution must preserve an existing instance and binding")
		}
		if target.TemplateVersion <= instance.TemplateVersion {
			return out, errors.New("target template version must follow the source instance version")
		}
		template, e := scenetemplates.Get(instance.TemplateID, target.TemplateVersion)
		if e != nil {
			return out, e
		}
		instance.TemplateVersion = target.TemplateVersion
		merged := map[string]any{}
		for k, v := range instance.Parameters {
			merged[k] = v
		}
		for k, v := range target.Parameters {
			merged[k] = v
		}
		instance.Parameters, e = scenetemplates.Parameters(template, merged)
		if e != nil {
			return out, e
		}
		deviceDoc, e := s.Business.read(ctx, "entity", binding.DeviceID, seen)
		if e != nil {
			return out, e
		}
		device, e := store.Decode[model.Entity](deviceDoc)
		if e != nil {
			return out, e
		}
		if deviceDoc.Version != binding.DeviceVersion || device.EdgeID != binding.SourceID || device.Status != "approved" {
			return out, fmt.Errorf("%w: template device binding has changed", store.ErrConflict)
		}
		for _, resource := range []string{binding.DeviceID, binding.SourceID} {
			if e = s.Business.access().permit(ctx, p, "publish", resource, seen); e != nil {
				return out, e
			}
		}
		if len(template.RequiredActions) > 0 {
			member, e := s.Business.member(ctx, instance.SafetyUserID, []string{binding.DeviceID, batch.GroupID}, seen)
			if e != nil {
				return out, e
			}
			if !slices.Contains(member.Roles, "safety") && !slices.Contains(member.Roles, "admin") {
				return out, errors.New("template control requires a current safety member")
			}
		}
		cfgDoc, e := s.Business.read(ctx, "connector_configuration_version", model.ConnectorSecretReference(binding.ConfigurationID, binding.ConfigurationVersion), seen)
		if e != nil {
			return out, e
		}
		cfg, e := store.Decode[model.ConnectorConfiguration](cfgDoc)
		if e != nil {
			return out, e
		}
		if cfg.EdgeID != binding.SourceID || cfg.Version != binding.ConfigurationVersion || cfg.Protocol != template.Protocol {
			return out, errors.New("fixed connector configuration differs from the template binding")
		}
		ref := model.ConfigurationReference{Kind: "connector", ID: cfg.ID, Version: cfg.Version, Digest: configcenter.ConnectorDigest(cfg)}
		cid := "connector:" + cfg.ID
		if !configurationIDs[cid] {
			configurationIDs[cid] = true
			manifest.Components = append(manifest.Components, model.ReleaseComponent{ID: cid, Kind: "configuration", Version: strconv.FormatInt(ref.Version, 10), SHA256: ref.Digest, Format: releasebundle.ConfigurationFormat, Configuration: &ref, TargetNodeIDs: []string{cfg.EdgeID}, RequiredCapabilities: []string{"connector:" + cfg.Protocol}, DependsOn: []model.ReleaseDependency{{ID: program.ID, Version: program.Version, SHA256: program.SHA256}}})
		}
		defs, e := scenetemplates.Instantiate(template, instance.ID, instance.Name, batch.GroupID, device.ID, device.EdgeID, instance.SafetyUserID, instance.Parameters)
		if e != nil {
			return out, e
		}
		for _, d := range defs {
			if e = s.Business.Definitions.access(ctx, p, d, "publish", seen); e != nil {
				return out, e
			}
			d.Version = 1
			if current, e := s.Business.read(ctx, "definition", d.ID, seen); e == nil {
				d.Version = current.Version + 1
			} else if !errors.Is(e, store.ErrNotFound) {
				return out, e
			}
			d.Status = "published"
			raw, e := json.Marshal(d)
			if e != nil {
				return out, e
			}
			digest, e := releasebundle.ContentDigest(raw)
			if e != nil {
				return out, e
			}
			manifest.Components = append(manifest.Components, model.ReleaseComponent{ID: d.ID, Kind: "rule", Version: strconv.FormatInt(d.Version, 10), SHA256: digest, Format: model.ContractVersion, Content: raw, TargetNodeIDs: []string{device.EdgeID}, DependsOn: []model.ReleaseDependency{{ID: cid, Version: strconv.FormatInt(ref.Version, 10), SHA256: ref.Digest}}})
			out.DefinitionIDs = append(out.DefinitionIDs, d.ID)
		}
		out.Instances = append(out.Instances, instance)
		out.Bindings = append(out.Bindings, binding)
		if !slices.Contains(out.DeviceIDs, device.ID) {
			out.DeviceIDs = append(out.DeviceIDs, device.ID)
		}
		if !slices.Contains(out.NodeIDs, device.EdgeID) {
			out.NodeIDs = append(out.NodeIDs, device.EdgeID)
		}
	}
	for _, ref := range in.Configurations {
		cid := ref.Kind + ":" + ref.ID
		if configurationIDs[cid] {
			return out, errors.New("configuration identity is repeated")
		}
		configurationIDs[cid] = true
		manifest.Components = append(manifest.Components, model.ReleaseComponent{ID: cid, Kind: "configuration", Version: strconv.FormatInt(ref.Version, 10), SHA256: ref.Digest, Format: releasebundle.ConfigurationFormat, Configuration: &ref, TargetNodeIDs: append([]string{}, out.NodeIDs...)})
	}
	manifest.Template = &model.ReleaseTemplateOrigin{BatchID: id, BatchVersion: batch.Version, Instances: out.Instances, Bindings: out.Bindings, DefinitionIDs: out.DefinitionIDs}
	validation, err := s.Validate(ctx, p, manifest)
	if err != nil {
		return out, err
	}
	if !validation.Valid {
		return out, releaseValidationError(validation)
	}
	authorized, err := s.authorizeManifest(ctx, p, manifest, "publish")
	if err != nil {
		return out, err
	}
	out.Release = model.Release{ID: manifest.ID, Manifest: manifest, SHA256: validation.SHA256, Order: validation.Order, Version: 1, CreatedBy: p.User.ID, CreatedMS: out.CreatedMS}
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if e := seen.check(tx); e != nil {
			return e
		}
		if e := authorized.check(tx); e != nil {
			return e
		}
		duplicate, e := tx.BusinessRequest(scope, in.RequestID, hash, &out)
		if e != nil || duplicate {
			return e
		}
		if _, e = tx.Put("release", out.Release.ID, 0, out.Release); e != nil {
			return e
		}
		if _, e = tx.Put("template_evolution", out.ID, 0, out); e != nil {
			return e
		}
		if e = tx.Audit(p.Actor, "template.evolve", batch.GroupID, out.ID, map[string]any{"batch_id": id, "batch_version": batch.Version, "release_id": out.Release.ID, "release_sha256": out.Release.SHA256, "instances": out.Instances, "bindings": out.Bindings}); e != nil {
			return e
		}
		return tx.CompleteBusinessRequest(scope, in.RequestID, hash, out)
	})
	return out, err
}

func (s *Releases) TemplateEvolutions(ctx context.Context, p identity.Principal, id string) ([]model.TemplateEvolution, error) {
	if _, e := s.Business.TemplateBatch(ctx, p, id); e != nil {
		return nil, e
	}
	docs, e := s.Store.List(ctx, "template_evolution")
	if e != nil {
		return nil, e
	}
	out := []model.TemplateEvolution{}
	for _, doc := range docs {
		v, e := store.Decode[model.TemplateEvolution](doc)
		if e != nil {
			return nil, e
		}
		if v.SourceBatchID != id {
			continue
		}
		if _, e = s.Get(ctx, p, v.Release.ID); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}

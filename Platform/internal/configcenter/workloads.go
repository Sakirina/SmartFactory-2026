package configcenter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func ParameterDigest(p Parameter) string {
	value := p.Value
	if p.Secret {
		value = nil
	}
	ref := p.CredentialRef
	if p.Secret && ref == "" {
		ref = "parameter:" + p.ID + ":" + fmt.Sprint(p.Version)
	}
	return store.Hash(map[string]any{"id": p.ID, "version": p.Version, "program": p.Program, "category": p.Category, "schema": p.Schema, "dynamic": p.Dynamic, "secret": p.Secret, "value": value, "credential_ref": ref, "target_node_ids": p.TargetNodeIDs})
}

func ConnectorDigest(c model.ConnectorConfiguration) string { return store.Hash(c) }

func (s *Service) workloadService() *nodeidentity.Service {
	if s.Workloads != nil {
		return s.Workloads
	}
	return &nodeidentity.Service{Store: s.Store, Cipher: s.Identity}
}

func allowsProgram(identityProgram, configProgram string) bool {
	return identityProgram == configProgram || configProgram == "platform" && (identityProgram == "cloud" || identityProgram == "edge")
}

func parameterEnvelope(p Parameter, w model.WorkloadIdentity) model.ConfigurationEnvelope {
	ref := p.CredentialRef
	if p.Secret && ref == "" {
		ref = "parameter:" + p.ID + ":" + fmt.Sprint(p.Version)
	}
	out := model.ConfigurationEnvelope{Reference: model.ConfigurationReference{Kind: "parameter", ID: p.ID, Version: p.Version, Digest: ParameterDigest(p)}, NodeID: w.NodeID, Program: w.Program, Purpose: w.Purpose, Dynamic: p.Dynamic, Schema: p.Schema, CredentialRef: ref}
	if !p.Secret {
		out.Value = p.Value
	}
	return out
}

func (s *Service) envelopeTx(tx *store.Tx, w model.WorkloadIdentity, ref model.ConfigurationReference) (model.ConfigurationEnvelope, error) {
	if ref.ID == "" || ref.Version < 1 {
		return model.ConfigurationEnvelope{}, errors.New("fixed configuration identity and version are required")
	}
	switch ref.Kind {
	case "parameter":
		if !slices.Contains(w.ParameterIDs, ref.ID) {
			return model.ConfigurationEnvelope{}, identity.ErrDenied
		}
		d, err := tx.Read("parameter_version", ref.ID+":"+fmt.Sprint(ref.Version))
		if errors.Is(err, store.ErrNotFound) {
			d, err = tx.Read("parameter", ref.ID)
		}
		if err != nil {
			return model.ConfigurationEnvelope{}, err
		}
		p, err := store.Decode[Parameter](d)
		if err != nil {
			return model.ConfigurationEnvelope{}, err
		}
		if p.Version != ref.Version {
			return model.ConfigurationEnvelope{}, store.ErrConflict
		}
		if !allowsProgram(w.Program, p.Program) || len(p.TargetNodeIDs) > 0 && !slices.Contains(p.TargetNodeIDs, w.NodeID) {
			return model.ConfigurationEnvelope{}, identity.ErrDenied
		}
		out := parameterEnvelope(p, w)
		if ref.Digest != "" && ref.Digest != out.Reference.Digest {
			return out, store.ErrConflict
		}
		return out, nil
	case "connector":
		if !slices.Contains(w.ConnectorIDs, ref.ID) {
			return model.ConfigurationEnvelope{}, identity.ErrDenied
		}
		d, err := tx.Read("connector_configuration_version", model.ConnectorSecretReference(ref.ID, ref.Version))
		if errors.Is(err, store.ErrNotFound) {
			d, err = tx.Read("connector_configuration", ref.ID)
		}
		if err != nil {
			return model.ConfigurationEnvelope{}, err
		}
		c, err := store.Decode[model.ConnectorConfiguration](d)
		if err != nil {
			return model.ConfigurationEnvelope{}, err
		}
		if c.Version != ref.Version {
			return model.ConfigurationEnvelope{}, store.ErrConflict
		}
		if c.EdgeID != w.NodeID || (w.Program != "edge" && w.Program != "gateway") || !slices.Contains(w.Capabilities, "connector:"+c.Protocol) {
			return model.ConfigurationEnvelope{}, identity.ErrDenied
		}
		out := model.ConfigurationEnvelope{Reference: model.ConfigurationReference{Kind: "connector", ID: c.ID, Version: c.Version, Digest: ConnectorDigest(c)}, NodeID: w.NodeID, Program: w.Program, Purpose: w.Purpose, Dynamic: true, Connector: &c, CredentialRef: c.CredentialRef}
		if ref.Digest != "" && ref.Digest != out.Reference.Digest {
			return out, store.ErrConflict
		}
		return out, nil
	default:
		return model.ConfigurationEnvelope{}, errors.New("configuration kind must be parameter or connector")
	}
}

func targetID(w model.WorkloadIdentity, ref model.ConfigurationReference) string {
	return w.ID + ":" + ref.Kind + ":" + ref.ID
}

func (s *Service) GetReleaseConfigurationTx(tx *store.Tx, p nodeidentity.Principal, refs []model.ConfigurationReference) ([]model.ConfigurationEnvelope, error) {
	w, err := s.workloadService().ValidateTx(tx, p, "config.read")
	if err != nil {
		return nil, err
	}
	if !slices.Contains(w.Capabilities, "config.v2") {
		return nil, identity.ErrDenied
	}
	if len(refs) > 512 {
		return nil, errors.New("configuration reference count exceeds 512")
	}
	out := make([]model.ConfigurationEnvelope, 0, len(refs))
	for _, ref := range refs {
		e, err := s.envelopeTx(tx, w, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// SetReleaseConfigurationTargetTx changes desired configuration only as part of
// an explicit authorized release assignment. Fixed version reads are side-effect free.
func (s *Service) SetReleaseConfigurationTargetTx(tx *store.Tx, p nodeidentity.Principal, refs []model.ConfigurationReference) error {
	w, err := s.workloadService().ValidateTx(tx, p, "release")
	if err != nil {
		return err
	}
	envelopes, err := s.GetReleaseConfigurationTx(tx, p, refs)
	if err != nil {
		return err
	}
	canonical := make([]model.ConfigurationReference, 0, len(envelopes))
	desired := map[string]bool{}
	for _, e := range envelopes {
		canonical = append(canonical, e.Reference)
		desired[targetID(w, e.Reference)] = true
	}
	// An explicit target is the complete desired set for this workload. Remove
	// earlier issued targets so a removed reference cannot still resolve secrets
	// or submit running reports. Other workload targets remain independent.
	priorTargets, err := tx.List("configuration_target")
	if err != nil {
		return err
	}
	for _, target := range priorTargets {
		var prior struct {
			IdentityID string `json:"identity_id"`
		}
		if err = store.DecodeJSON(target.Data, &prior); err != nil {
			return err
		}
		if prior.IdentityID == w.ID && !desired[target.ID] {
			if err = tx.Delete("configuration_target", target.ID); err != nil {
				return err
			}
		}
	}
	if err = tx.SetEphemeral("configuration_release_target", w.ID, map[string]any{"references": canonical}); err != nil {
		return err
	}
	for _, e := range envelopes {
		if err = s.setTargetTx(tx, w, e); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) setTargetTx(tx *store.Tx, w model.WorkloadIdentity, e model.ConfigurationEnvelope) error {
	return tx.SetEphemeral("configuration_target", targetID(w, e.Reference), map[string]any{"reference": e.Reference, "identity_id": w.ID, "generation": w.Generation, "instance_epoch": w.InstanceEpoch})
}

func (s *Service) GetReleaseConfiguration(ctx context.Context, p nodeidentity.Principal, refs []model.ConfigurationReference) ([]model.ConfigurationEnvelope, error) {
	if err := s.EnsureConnectorVersions(ctx, refs); err != nil {
		return nil, err
	}
	var out []model.ConfigurationEnvelope
	err := s.Store.Write(ctx, func(tx *store.Tx) error { var e error; out, e = s.GetReleaseConfigurationTx(tx, p, refs); return e })
	return out, err
}

func (s *Service) WorkloadSnapshot(ctx context.Context, p nodeidentity.Principal) ([]model.ConfigurationEnvelope, error) {
	if err := s.ensureWorkloadConnectors(ctx, p); err != nil {
		return nil, err
	}
	var out []model.ConfigurationEnvelope
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		w, e := s.workloadService().ValidateTx(tx, p, "config.read")
		if e != nil {
			return e
		}
		refs := []model.ConfigurationReference{}
		if d, e := tx.Read("configuration_release_target", w.ID); e == nil {
			var fixed struct {
				References []model.ConfigurationReference `json:"references"`
			}
			if e = store.DecodeJSON(d.Data, &fixed); e != nil {
				return e
			}
			out, e = s.GetReleaseConfigurationTx(tx, p, fixed.References)
			if e != nil {
				return e
			}
			for _, env := range out {
				if e = s.setTargetTx(tx, w, env); e != nil {
					return e
				}
			}
			return nil
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if w.Purpose == "release-runtime" {
			return identity.ErrDenied
		}
		for _, id := range w.ParameterIDs {
			d, e := tx.Read("parameter", id)
			if errors.Is(e, store.ErrNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			v, e := store.Decode[Parameter](d)
			if e != nil {
				return e
			}
			if allowsProgram(w.Program, v.Program) && (len(v.TargetNodeIDs) == 0 || slices.Contains(v.TargetNodeIDs, w.NodeID)) {
				refs = append(refs, model.ConfigurationReference{Kind: "parameter", ID: id, Version: v.Version})
			}
		}
		for _, id := range w.ConnectorIDs {
			d, e := tx.Read("connector_configuration", id)
			if errors.Is(e, store.ErrNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			v, e := store.Decode[model.ConnectorConfiguration](d)
			if e != nil {
				return e
			}
			if v.EdgeID == w.NodeID && (w.Program == "edge" || w.Program == "gateway") && slices.Contains(w.Capabilities, "connector:"+v.Protocol) {
				refs = append(refs, model.ConfigurationReference{Kind: "connector", ID: id, Version: v.Version})
			}
		}
		out, e = s.GetReleaseConfigurationTx(tx, p, refs)
		if e == nil {
			for _, env := range out {
				if e = s.setTargetTx(tx, w, env); e != nil {
					return e
				}
			}
		}
		return e
	})
	return out, err
}

func (s *Service) ResolveCredential(ctx context.Context, p nodeidentity.Principal, input model.CredentialResolution) (model.CredentialPayload, error) {
	if input.Reference.Kind == "connector" && s.OwnedNodeID == "" && s.workloadService().AuthorityURL != "" {
		return s.resolveRemoteConnector(ctx, p, input)
	}
	var out model.CredentialPayload
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		w, e := s.workloadService().ValidateTx(tx, p, "credential.resolve")
		if e != nil {
			return e
		}
		if input.Purpose != w.Purpose {
			return identity.ErrDenied
		}
		if !slices.Contains(w.Capabilities, "config.read") || !slices.Contains(w.Capabilities, "config.v2") {
			return identity.ErrDenied
		}
		if s.OwnedNodeID != "" && (w.NodeID != s.OwnedNodeID || input.Reference.Kind != "connector") {
			return identity.ErrDenied
		}
		if s.OwnedNodeID == "" {
			if e = s.requireCredentialTargetTx(tx, p, input.Reference); e != nil {
				return e
			}
		}
		envelope, e := s.envelopeTx(tx, w, input.Reference)
		if e != nil {
			return e
		}
		if input.CredentialRef == "" || envelope.CredentialRef != input.CredentialRef {
			return identity.ErrDenied
		}
		var raw string
		if input.Reference.Kind == "parameter" {
			d, e := tx.Read("parameter_version", input.Reference.ID+":"+fmt.Sprint(input.Reference.Version))
			if errors.Is(e, store.ErrNotFound) {
				d, e = tx.Read("parameter", input.Reference.ID)
			}
			if e != nil {
				return e
			}
			v, e := store.Decode[Parameter](d)
			if e != nil {
				return e
			}
			m, ok := v.Value.(map[string]any)
			if !ok {
				return errors.New("encrypted parameter payload is unavailable")
			}
			cipher, _ := m["ciphertext"].(string)
			raw, e = s.Identity.Decrypt("config:"+v.ID, cipher)
			if e != nil {
				return e
			}
			var value any
			if e = store.DecodeJSON([]byte(raw), &value); e != nil {
				return e
			}
			if e = Validate(v.Schema, value); e != nil {
				return errors.New("secret payload schema mismatch")
			}
		} else {
			d, e := tx.Read("connector_configuration_secret", input.CredentialRef)
			if e != nil {
				return e
			}
			var secret struct {
				Ciphertext      string `json:"ciphertext"`
				ConfigurationID string `json:"configuration_id"`
				Version         int64  `json:"version"`
			}
			if e = store.DecodeJSON(d.Data, &secret); e != nil {
				return e
			}
			if secret.ConfigurationID != input.Reference.ID || secret.Version != input.Reference.Version || input.CredentialRef != model.ConnectorSecretReference(secret.ConfigurationID, secret.Version) {
				return store.ErrConflict
			}
			raw, e = s.Identity.Decrypt("connector:"+input.CredentialRef, secret.Ciphertext)
			if e != nil {
				return e
			}
			v, e := deviceconfig.Validate(deviceconfig.Request{Protocol: envelope.Connector.Protocol, Config: json.RawMessage(raw)})
			if e != nil {
				return e
			}
			public, e := deviceconfig.PublicConfiguration(v)
			if e != nil {
				return e
			}
			var actual, expected any
			if e = store.DecodeJSON(public.Config, &actual); e != nil {
				return e
			}
			if e = store.DecodeJSON(envelope.Connector.Config, &expected); e != nil {
				return e
			}
			if store.Hash(actual) != store.Hash(expected) {
				return store.ErrConflict
			}
		}
		out = model.CredentialPayload{Reference: envelope.Reference, CredentialRef: input.CredentialRef, Payload: json.RawMessage(raw)}
		return tx.Audit(p.Actor(), "workload.credential.resolve", w.NodeID, "", map[string]any{"reference": envelope.Reference, "credential_ref": input.CredentialRef, "purpose": input.Purpose})
	})
	return out, err
}

func (s *Service) ReportConfiguration(ctx context.Context, p nodeidentity.Principal, r model.ConfigurationReport) (model.ConfigurationReport, error) {
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		w, e := s.workloadService().ValidateTx(tx, p, "config.report")
		if e != nil {
			return e
		}
		if r.IdentityID != w.ID || r.NodeID != w.NodeID || r.Program != w.Program || r.Purpose != w.Purpose || r.Generation != w.Generation || r.InstanceID != w.InstanceID || r.InstanceEpoch != w.InstanceEpoch {
			return identity.ErrDenied
		}
		if r.Sequence < 1 {
			return errors.New("positive report sequence is required")
		}
		ref := model.ConfigurationReference{Kind: r.Kind, ID: r.ID, Version: r.Desired.Version, Digest: r.Desired.Digest}
		envelope, e := s.envelopeTx(tx, w, ref)
		if e != nil {
			return e
		}
		target, e := tx.Read("configuration_target", targetID(w, ref))
		if e != nil {
			return e
		}
		var expected struct {
			Reference     model.ConfigurationReference `json:"reference"`
			Generation    int64                        `json:"generation"`
			InstanceEpoch int64                        `json:"instance_epoch"`
		}
		if e = store.DecodeJSON(target.Data, &expected); e != nil {
			return e
		}
		if expected.Reference != ref || expected.Generation != w.Generation || expected.InstanceEpoch != w.InstanceEpoch {
			return store.ErrConflict
		}
		want := model.ConfigurationVersion{Version: ref.Version, Digest: ref.Digest}
		if r.Prepared != want {
			return store.ErrConflict
		}
		if envelope.CredentialRef != "" && r.EffectiveValue != nil {
			return identity.ErrDenied
		}
		if len(r.Reason) > 128 || strings.ContainsAny(r.Reason, "\r\n") {
			return errors.New("bounded failure code is required")
		}
		var previous model.ConfigurationReport
		var oldEpoch, oldSequence int64
		var previousHash, raw string
		e = tx.QueryRowContext(tx.Ctx, "SELECT instance_epoch,sequence,report_hash,data FROM sf_configuration_reports WHERE identity_id=$1 AND kind=$2 AND configuration_id=$3", w.ID, r.Kind, r.ID).Scan(&oldEpoch, &oldSequence, &previousHash, &raw)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		exists := e == nil
		if exists {
			if e = store.DecodeJSON([]byte(raw), &previous); e != nil {
				return e
			}
		}
		r.AtMS = 0
		hash := store.Hash(r)
		if exists && oldEpoch == w.InstanceEpoch {
			if r.Sequence == oldSequence && hash == previousHash {
				r = previous
				return nil
			}
			if r.Sequence <= oldSequence {
				return store.ErrConflict
			}
		}
		switch r.State {
		case "running":
			if r.Applied != want || r.Running != want {
				return store.ErrConflict
			}
			if envelope.Reference.Kind == "parameter" && envelope.CredentialRef == "" && store.Hash(r.EffectiveValue) != store.Hash(envelope.Value) {
				return store.ErrConflict
			}
		case "prepared", "failed", "restart_required":
			if exists && (r.Applied != previous.Applied || r.Running != previous.Running) {
				return store.ErrConflict
			}
			if !exists && (r.Applied.Version != 0 || r.Running.Version != 0) {
				return store.ErrConflict
			}
			if r.State == "restart_required" && envelope.Dynamic {
				return store.ErrConflict
			}
		default:
			return errors.New("invalid configuration state")
		}
		r.AtMS = s.Store.Now().UnixMilli()
		data, e := json.Marshal(r)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(tx.Ctx, `INSERT INTO sf_configuration_reports(identity_id,kind,configuration_id,instance_epoch,sequence,report_hash,data) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(identity_id,kind,configuration_id) DO UPDATE SET instance_epoch=excluded.instance_epoch,sequence=excluded.sequence,report_hash=excluded.report_hash,data=excluded.data`, w.ID, r.Kind, r.ID, w.InstanceEpoch, r.Sequence, hash, string(data)); e != nil {
			return e
		}
		if e = tx.SetEphemeral("configuration_report", targetID(w, ref), r); e != nil {
			return e
		}
		return tx.Audit(p.Actor(), "workload.configuration.report", w.NodeID, "", r)
	})
	return r, err
}

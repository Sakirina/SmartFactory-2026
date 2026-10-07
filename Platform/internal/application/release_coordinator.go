package application

import (
	"context"
	"errors"
	"strconv"

	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *Releases) checkConfiguration(ctx context.Context, ref model.ConfigurationReference) error {
	if s.ConfigurationMetadata != nil {
		_, err := s.ConfigurationMetadata(ctx, []model.ConfigurationReference{ref})
		return err
	}
	kind := "parameter_version"
	if ref.Kind == "connector" {
		kind = "connector_configuration_version"
	}
	doc, err := s.Store.Get(ctx, kind, ref.ID+":"+strconv.FormatInt(ref.Version, 10))
	if err != nil {
		return err
	}
	var digest string
	if ref.Kind == "parameter" {
		p, e := store.Decode[configcenter.Parameter](doc)
		if e != nil {
			return e
		}
		digest = configcenter.ParameterDigest(p)
	} else {
		c, e := store.Decode[model.ConnectorConfiguration](doc)
		if e != nil {
			return e
		}
		digest = configcenter.ConnectorDigest(c)
	}
	if digest != ref.Digest {
		return errors.New("configuration version digest differs from the immutable reference")
	}
	return nil
}

func releaseBusinessHash(d model.ReleaseDeployment) string {
	targets := []any{}
	for _, t := range d.Targets {
		state := t.State
		if state == "waiting" || state == "offline" || state == "preparing" {
			state = "pending"
		}
		targets = append(targets, []any{t.IdentityID, t.Generation, t.Batch, state, t.DesiredSHA256, t.FailureComponent, t.Reason})
	}
	batches := []any{}
	for _, b := range d.Batches {
		batches = append(batches, []any{b.Index, b.IdentityIDs, b.State})
	}
	return store.Hash([]any{d.State, d.CurrentBatch, d.ReleaseSHA256, targets, batches})
}

func (s *Releases) RegisterArtifact(ctx context.Context, p identity.Principal, in RegisterReleaseArtifactInput) (model.ReleaseArtifact, error) {
	seen, err := s.Business.authorize(ctx, p, "publish", nil)
	if err != nil {
		return model.ReleaseArtifact{}, err
	}
	return releasebundle.RegisterArtifact(ctx, s.Store, s.ArtifactRoot, in.SHA256, in.Build, p.Actor, seen.check)
}

func (s *Releases) Desired(ctx context.Context, p nodeidentity.Principal) (model.ReleaseDesired, error) {
	result := model.ReleaseDesired{Action: "wait", Configurations: []model.ConfigurationEnvelope{}}
	// Resolve only the assignment identity outside the transaction. It is read
	// again under the same lock order as reports before any content is returned.
	adoc, err := s.Store.Get(ctx, "release_assignment", p.Identity.ID)
	if errors.Is(err, store.ErrNotFound) {
		err = s.Store.Write(ctx, func(tx *store.Tx) error { _, e := s.Nodes.ValidateTx(tx, p, "release"); return e })
		return result, err
	}
	if err != nil {
		return result, err
	}
	a, err := store.Decode[model.ReleaseAssignment](adoc)
	if err != nil {
		return result, err
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		n, e := s.Nodes.ValidateTx(tx, p, "release")
		if e != nil {
			return e
		}
		doc, e := tx.Get("release_deployment", a.DeploymentID)
		if e != nil {
			return e
		}
		d, e := store.Decode[model.ReleaseDeployment](doc)
		if e != nil {
			return e
		}
		current, e := tx.Get("release_assignment", p.Identity.ID)
		if e != nil {
			return e
		}
		if current.Version != adoc.Version {
			return store.ErrConflict
		}
		index := -1
		for i, t := range d.Targets {
			if t.IdentityID == p.Identity.ID && t.Generation == a.Generation {
				index = i
				break
			}
		}
		if index < 0 {
			return identity.ErrDenied
		}
		target := &d.Targets[index]
		result.DeploymentID = d.ID
		result.Generation = a.Generation
		result.Target = target
		if target.Batch > d.CurrentBatch || d.State == "paused" || d.State == "cancelled" || d.State == "failed" {
			return nil
		}
		rdoc, e := tx.Get("release", d.ReleaseID)
		if e != nil {
			return e
		}
		r, e := decodeRelease(rdoc)
		if e != nil {
			return e
		}
		if e = s.checkTarget(r, n); e != nil {
			return e
		}
		// The agent requests the fixed configuration from the independent center
		// after this transaction commits. The center calls AuthorizeConfigurationTarget.
		result.Available = true
		result.Release = &r
		result.Action = "prepare"
		if target.PreparedSHA256 == r.SHA256 {
			result.Action = "apply"
		}
		if target.RunningSHA256 == r.SHA256 {
			result.Action = "verify"
		}
		target.LastSeenMS = s.Store.CurrentTime().UnixMilli()
		if target.State == "waiting" || target.State == "offline" {
			target.State = "preparing"
		}
		d.UpdatedMS = target.LastSeenMS
		d.AllowedActions = nil
		_, e = tx.Put("release_deployment", d.ID, doc.Version, d)
		return e
	})
	return result, err
}

func (s *Releases) ArtifactForNode(ctx context.Context, p nodeidentity.Principal, digest string) (string, error) {
	desired, err := s.Desired(ctx, p)
	if err != nil {
		return "", err
	}
	if !desired.Available || desired.Release == nil || releasebundle.Program(desired.Release.Manifest).SHA256 != digest {
		return "", identity.ErrDenied
	}
	if _, err = releasebundle.VerifyArtifact(s.ArtifactRoot, digest); err != nil {
		return "", err
	}
	return releasebundle.ArtifactPath(s.ArtifactRoot, digest)
}

func (s *Releases) Reconcile(ctx context.Context) error {
	if s.Mode != "cloud" {
		return nil
	}
	docs, err := s.Store.List(ctx, "release_deployment")
	if err != nil {
		return err
	}
	for _, doc := range docs {
		var d model.ReleaseDeployment
		if d, err = store.Decode[model.ReleaseDeployment](doc); err != nil {
			return err
		}
		if d.State != "active" {
			continue
		}
		if err = s.reconcileOne(ctx, d.ID); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}
func (s *Releases) reconcileOne(ctx context.Context, id string) error {
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		doc, err := tx.Get("release_deployment", id)
		if err != nil {
			return err
		}
		d, err := store.Decode[model.ReleaseDeployment](doc)
		if err != nil {
			return err
		}
		if d.State != "active" {
			return nil
		}
		before := store.Hash(d)
		oldBusiness := releaseBusinessHash(d)
		now := s.Store.CurrentTime().UnixMilli()
		offline := s.OfflineAfterMS
		if offline <= 0 {
			offline = 15000
		}
		ready := true
		for i := range d.Targets {
			target := &d.Targets[i]
			if target.Batch != d.CurrentBatch {
				continue
			}
			n, e := s.Nodes.LookupTx(tx, target.IdentityID)
			if e != nil || !n.Enabled {
				target.State = "failed"
				target.Reason = "workload identity is unavailable or disabled"
				d.State = "failed"
				d.Batches[d.CurrentBatch].State = "failed"
				ready = false
				continue
			}
			if target.State == "failed" {
				d.State = "failed"
				d.Batches[d.CurrentBatch].State = "failed"
				ready = false
				continue
			}
			if target.State != "running" || target.RunningSHA256 != d.ReleaseSHA256 || target.AgentInstanceEpoch != n.InstanceEpoch {
				ready = false
				if target.LastSeenMS == 0 || now-target.LastSeenMS > offline {
					target.State = "offline"
				}
			}
			if target.LastReportMS == 0 || now-target.LastReportMS > offline {
				ready = false
				if target.State != "failed" {
					target.State = "offline"
				}
			}
		}
		if ready {
			batch := &d.Batches[d.CurrentBatch]
			batch.State = "completed"
			batch.CompletedMS = now
			if d.CurrentBatch+1 == len(d.Batches) {
				d.State = "completed"
			} else {
				d.CurrentBatch++
				next := &d.Batches[d.CurrentBatch]
				next.State = "active"
				next.EnteredMS = now
				for i := range d.Targets {
					if d.Targets[i].Batch == d.CurrentBatch {
						d.Targets[i].State = "waiting"
					}
				}
			}
		}
		if store.Hash(d) == before {
			return nil
		}
		if releaseBusinessHash(d) != oldBusiness {
			d.Version++
		}
		d.UpdatedMS = now
		d.AllowedActions = nil
		if _, err = tx.Put("release_deployment", id, doc.Version, d); err != nil {
			return err
		}
		if releaseBusinessHash(d) == oldBusiness {
			return nil
		}
		return tx.Audit(model.Actor{UserID: "system", Source: "release-coordinator"}, "release.deployment.reconcile", d.GroupID, id, map[string]any{"state": d.State, "batch": d.CurrentBatch, "version": d.Version})
	})
}

func (s *Releases) Reports(ctx context.Context, p identity.Principal, id string) ([]model.ReleaseNodeReport, error) {
	if _, err := s.Deployment(ctx, p, id); err != nil {
		return nil, err
	}
	rows, err := s.Store.DB.QueryContext(ctx, "SELECT data FROM sf_release_reports WHERE deployment_id=$1 ORDER BY received_ms DESC,identity_id,instance_epoch DESC,sequence DESC LIMIT 1000", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ReleaseNodeReport{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r model.ReleaseNodeReport
		if err = store.DecodeJSON([]byte(raw), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

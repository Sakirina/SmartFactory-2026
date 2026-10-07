package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Releases struct {
	Store                 *store.Store
	Business              *Business
	Nodes                 *nodeidentity.Service
	Config                *configcenter.Service
	ArtifactRoot          string
	Mode                  string
	OfflineAfterMS        int64
	ConfigurationMetadata func(context.Context, []model.ConfigurationReference) ([]model.ConfigurationMetadata, error)
}

type CreateReleaseInput struct {
	RequestID string                `json:"request_id" required:"true"`
	Manifest  model.ReleaseManifest `json:"manifest" required:"true"`
}
type CreateReleaseDeploymentInput struct {
	ID        string     `json:"id" required:"true"`
	RequestID string     `json:"request_id" required:"true"`
	ReleaseID string     `json:"release_id" required:"true"`
	GroupID   string     `json:"group_id" required:"true"`
	Batches   [][]string `json:"batches" minItems:"1" maxItems:"20" required:"true"`
	Reason    string     `json:"reason" required:"true"`
}
type ReleaseDeploymentActionInput struct {
	RequestID       string `json:"request_id" required:"true"`
	ExpectedVersion int64  `json:"expected_version" minimum:"1" required:"true"`
	Action          string `json:"action" enum:"pause,resume,retry,cancel" required:"true"`
	Reason          string `json:"reason" required:"true"`
}
type ReleaseRollbackInput struct {
	RequestID       string `json:"request_id" required:"true"`
	ExpectedVersion int64  `json:"expected_version" minimum:"1" required:"true"`
	ID              string `json:"id" required:"true"`
	ReleaseID       string `json:"release_id" required:"true"`
	Reason          string `json:"reason" required:"true"`
}
type RegisterReleaseArtifactInput struct {
	SHA256 string             `json:"sha256" required:"true"`
	Build  model.ProgramBuild `json:"build" required:"true"`
}

func (s *Releases) authorizeManifest(ctx context.Context, p identity.Principal, m model.ReleaseManifest, action string) (*revisions, error) {
	seen, err := s.Business.authorize(ctx, p, action, nil)
	if err != nil {
		return nil, err
	}
	for _, c := range m.Components {
		if c.Kind == "rule" {
			var d model.Definition
			if err = store.DecodeJSON(c.Content, &d); err != nil {
				return nil, err
			}
			if err = s.Business.Definitions.access(ctx, p, d, action, seen); err != nil {
				return nil, err
			}
		}
		if s.ConfigurationMetadata == nil && c.Configuration != nil && c.Configuration.Kind == "connector" {
			doc, e := s.Store.Get(ctx, "connector_configuration_version", c.Configuration.ID+":"+strconv.FormatInt(c.Configuration.Version, 10))
			if e != nil {
				return nil, e
			}
			cfg, e := store.Decode[model.ConnectorConfiguration](doc)
			if e != nil {
				return nil, e
			}
			for _, id := range []string{cfg.GroupID, cfg.EdgeID} {
				if e = s.Business.access().permit(ctx, p, action, id, seen); e != nil {
					return nil, e
				}
			}
		}
	}
	if s.ConfigurationMetadata != nil {
		metadata, e := s.ConfigurationMetadata(ctx, releasebundle.References(m))
		if e != nil {
			return nil, e
		}
		for _, item := range metadata {
			resources := append([]string{}, item.NodeIDs...)
			if item.GroupID != "" {
				resources = append(resources, item.GroupID)
			}
			for _, id := range resources {
				if e = s.Business.access().permit(ctx, p, action, id, seen); e != nil {
					return nil, e
				}
			}
		}
	}
	return seen, nil
}

func (s *Releases) Validate(ctx context.Context, p identity.Principal, m model.ReleaseManifest) (model.ReleaseValidation, error) {
	v := releasebundle.Validate(ctx, m)
	if _, err := s.Business.authorize(ctx, p, "publish", nil); err != nil {
		return v, err
	}
	if !v.Valid {
		return v, nil
	}
	seen, err := s.authorizeManifest(ctx, p, m, "publish")
	if err != nil {
		return v, err
	}
	for _, c := range m.Components {
		if c.Kind == "program" {
			a, e := releasebundle.Artifact(ctx, s.Store, c.SHA256)
			if e == nil {
				_, e = releasebundle.VerifyArtifact(s.ArtifactRoot, c.SHA256)
			}
			if e == nil && store.Hash(a.Build) != store.Hash(c.Build) {
				e = errors.New("registered executable build differs from release metadata")
			}
			if e != nil {
				v.Valid = false
				v.Issues = append(v.Issues, model.ReleaseIssue{ComponentID: c.ID, Code: "artifact", Message: e.Error(), Recovery: []string{}})
			}
		}
		if c.Configuration != nil {
			if e := s.checkConfiguration(ctx, *c.Configuration); e != nil {
				v.Valid = false
				v.Issues = append(v.Issues, model.ReleaseIssue{ComponentID: c.ID, Code: "configuration", Message: e.Error(), Recovery: []string{}})
			}
		}
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
	return v, err
}

func releaseValidationError(v model.ReleaseValidation) error {
	messages := []string{}
	for _, x := range v.Issues {
		messages = append(messages, x.ComponentID+": "+x.Code+": "+x.Message)
	}
	return errors.New(strings.Join(messages, "; "))
}

func (s *Releases) Create(ctx context.Context, p identity.Principal, in CreateReleaseInput) (model.Release, error) {
	if in.Manifest.Template != nil {
		return model.Release{}, errors.New("template origin is assigned by the template evolution operation")
	}
	return s.create(ctx, p, in)
}
func (s *Releases) create(ctx context.Context, p identity.Principal, in CreateReleaseInput) (model.Release, error) {
	var result model.Release
	if s.Mode != "cloud" {
		return result, errors.New("releases are created in the cloud")
	}
	if err := businessID(in.RequestID); err != nil {
		return result, err
	}
	v, err := s.Validate(ctx, p, in.Manifest)
	if err != nil {
		return result, err
	}
	if !v.Valid {
		return result, releaseValidationError(v)
	}
	seen, err := s.authorizeManifest(ctx, p, in.Manifest, "publish")
	if err != nil {
		return result, err
	}
	result = model.Release{ID: in.Manifest.ID, Manifest: in.Manifest, SHA256: v.SHA256, Order: v.Order, CreatedMS: s.Store.CurrentTime().UnixMilli(), CreatedBy: p.User.ID, Version: 1}
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if e := seen.check(tx); e != nil {
			return e
		}
		scope, hash := "release-create:"+p.User.ID, store.Hash(in)
		duplicate, e := tx.BusinessRequest(scope, in.RequestID, hash, &result)
		if e != nil || duplicate {
			return e
		}
		old, e := tx.Get("release", result.ID)
		if e == nil {
			prior, e := store.Decode[model.Release](old)
			if e != nil {
				return e
			}
			if prior.SHA256 != result.SHA256 {
				return fmt.Errorf("%w: release identity already has different content", store.ErrConflict)
			}
			result = prior
			return tx.CompleteBusinessRequest(scope, in.RequestID, hash, result)
		}
		if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if _, e = tx.Put("release", result.ID, 0, result); e != nil {
			return e
		}
		if e = tx.Audit(p.Actor, "release.create", "", result.ID, map[string]any{"release_id": result.ID, "sha256": result.SHA256, "components": len(result.Manifest.Components)}); e != nil {
			return e
		}
		return tx.CompleteBusinessRequest(scope, in.RequestID, hash, result)
	})
	return result, err
}

func (s *Releases) Get(ctx context.Context, p identity.Principal, id string) (model.Release, error) {
	doc, err := s.Store.Get(ctx, "release", id)
	if err != nil {
		return model.Release{}, err
	}
	r, err := store.Decode[model.Release](doc)
	if err != nil {
		return r, err
	}
	seen, err := s.authorizeManifest(ctx, p, r.Manifest, "read")
	if err != nil {
		return model.Release{}, err
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
	return r, err
}
func (s *Releases) List(ctx context.Context, p identity.Principal) ([]model.Release, error) {
	if _, err := s.Business.authorize(ctx, p, "read", nil); err != nil {
		return nil, err
	}
	docs, err := s.Store.List(ctx, "release")
	if err != nil {
		return nil, err
	}
	out := []model.Release{}
	for _, d := range docs {
		r, e := s.Get(ctx, p, d.ID)
		if errors.Is(e, identity.ErrDenied) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
func (s *Releases) Artifacts(ctx context.Context, p identity.Principal) ([]model.ReleaseArtifact, error) {
	seen, err := s.Business.authorize(ctx, p, "publish", nil)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.DB.QueryContext(ctx, "SELECT sha256 FROM sf_release_artifacts ORDER BY sha256")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []model.ReleaseArtifact{}
	for _, id := range ids {
		a, e := releasebundle.Artifact(ctx, s.Store, id)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
	return out, err
}

func (s *Releases) checkTarget(r model.Release, n model.WorkloadIdentity) error {
	build := releasebundle.Program(r.Manifest).Build
	if build == nil || !n.Enabled || n.Program != r.Manifest.Program || !slices.Contains(n.Capabilities, "release") {
		return fmt.Errorf("workload %s does not support this release", n.ID)
	}
	for _, capability := range []string{"goos:" + build.GOOS, "goarch:" + build.GOARCH} {
		if !slices.Contains(n.Capabilities, capability) {
			return fmt.Errorf("workload %s lacks %s", n.ID, capability)
		}
	}
	rules, configs := 0, 0
	for _, c := range releasebundle.ComponentsForNode(r.Manifest, n.NodeID) {
		if c.Kind == "rule" {
			rules++
		}
		if c.Configuration != nil {
			configs++
			if c.Configuration.Kind == "parameter" && !slices.Contains(n.ParameterIDs, c.Configuration.ID) || c.Configuration.Kind == "connector" && !slices.Contains(n.ConnectorIDs, c.Configuration.ID) {
				return fmt.Errorf("workload %s lacks configuration scope %s", n.ID, c.Configuration.ID)
			}
		}
		for _, capability := range c.RequiredCapabilities {
			if !slices.Contains(n.Capabilities, capability) {
				return fmt.Errorf("workload %s lacks component %s capability %s", n.ID, c.ID, capability)
			}
		}
	}
	if rules == 0 || configs == 0 {
		return fmt.Errorf("workload %s has no applicable rule or configuration component", n.ID)
	}
	return nil
}

func (s *Releases) CreateDeployment(ctx context.Context, p identity.Principal, in CreateReleaseDeploymentInput) (model.ReleaseDeployment, error) {
	return s.createDeployment(ctx, p, in, "", 0)
}

func (s *Releases) configurationMetadata(ctx context.Context, refs []model.ConfigurationReference) ([]model.ConfigurationMetadata, error) {
	if s.ConfigurationMetadata != nil {
		return s.ConfigurationMetadata(ctx, refs)
	}
	if s.Config == nil {
		return nil, errors.New("configuration metadata service is unavailable")
	}
	return s.Config.ConfigurationMetadata(ctx, refs)
}

func checkConfigurationTargets(r model.Release, n model.WorkloadIdentity, metadata []model.ConfigurationMetadata) error {
	for _, ref := range releasebundle.ReferencesForNode(r.Manifest, n.NodeID) {
		index := slices.IndexFunc(metadata, func(m model.ConfigurationMetadata) bool { return m.Reference == ref })
		if index < 0 {
			return fmt.Errorf("fixed configuration metadata is missing for %s", ref.ID)
		}
		m := metadata[index]
		if len(m.NodeIDs) > 0 && !slices.Contains(m.NodeIDs, n.NodeID) {
			return fmt.Errorf("configuration %s does not apply to workload node %s", ref.ID, n.NodeID)
		}
		allowed := m.Program == n.Program || m.Program == "platform" && (n.Program == "cloud" || n.Program == "edge")
		if ref.Kind == "connector" {
			allowed = n.Program == "edge" || n.Program == "gateway"
		}
		if !allowed {
			return fmt.Errorf("configuration %s does not apply to workload program %s", ref.ID, n.Program)
		}
		for _, capability := range m.RequiredCapabilities {
			if !slices.Contains(n.Capabilities, capability) {
				return fmt.Errorf("workload %s lacks configuration %s capability %s", n.ID, ref.ID, capability)
			}
		}
	}
	return nil
}

func (s *Releases) createDeployment(ctx context.Context, p identity.Principal, in CreateReleaseDeploymentInput, rollbackOf string, expected int64) (model.ReleaseDeployment, error) {
	var result model.ReleaseDeployment
	if s.Mode != "cloud" {
		return result, errors.New("deployments are created in the cloud")
	}
	for _, id := range []string{in.ID, in.RequestID, in.GroupID, in.ReleaseID} {
		if err := businessID(id); err != nil {
			return result, err
		}
	}
	if err := businessReason(in.Reason); err != nil {
		return result, err
	}
	if len(in.Batches) < 1 || len(in.Batches) > 20 {
		return result, errors.New("deployment requires 1 to 20 batches")
	}
	r, err := s.Get(ctx, p, in.ReleaseID)
	if err != nil {
		return result, err
	}
	v, err := s.Validate(ctx, p, r.Manifest)
	if err != nil {
		return result, err
	}
	if !v.Valid {
		return result, releaseValidationError(v)
	}
	metadata, err := s.configurationMetadata(ctx, releasebundle.References(r.Manifest))
	if err != nil {
		return result, err
	}
	seen, err := s.Business.authorize(ctx, p, "publish", []string{in.GroupID})
	if err != nil {
		return result, err
	}
	result = model.ReleaseDeployment{ID: in.ID, ReleaseID: r.ID, ReleaseSHA256: r.SHA256, GroupID: in.GroupID, Version: 1, State: "active", CurrentBatch: 0, CreatedMS: s.Store.CurrentTime().UnixMilli(), CreatedBy: p.User.ID, RequestSHA256: store.Hash([]any{in, rollbackOf, expected}), RollbackOf: rollbackOf, Reason: in.Reason, Batches: []model.ReleaseBatch{}, Targets: []model.ReleaseTarget{}, AllowedActions: []model.BusinessAction{}}
	result.UpdatedMS = result.CreatedMS
	ids := map[string]bool{}
	identities := map[string]model.WorkloadIdentity{}
	for i, batch := range in.Batches {
		if len(batch) < 1 || len(batch) > 64 {
			return result, errors.New("a release batch requires 1 to 64 workloads")
		}
		b := model.ReleaseBatch{Index: i, IdentityIDs: append([]string{}, batch...), State: "pending", EntryCondition: "all workloads in the previous batch report the desired running content"}
		if i == 0 {
			b.State = "active"
			b.EnteredMS = result.CreatedMS
			b.EntryCondition = "release and every target capability have been validated"
		}
		result.Batches = append(result.Batches, b)
		for _, id := range batch {
			if ids[id] || len(ids) >= 128 {
				return result, errors.New("deployment requires distinct targets and has a 128 workload budget")
			}
			ids[id] = true
			n, e := s.Nodes.Lookup(ctx, id)
			if e != nil {
				return result, e
			}
			if e = s.checkTarget(r, n); e != nil {
				return result, e
			}
			if e = checkConfigurationTargets(r, n, metadata); e != nil {
				return result, e
			}
			if e = s.Business.access().permit(ctx, p, "publish", n.NodeID, seen); e != nil {
				return result, e
			}
			identities[id] = n
			state := "pending"
			if i == 0 {
				state = "waiting"
			}
			result.Targets = append(result.Targets, model.ReleaseTarget{IdentityID: id, NodeID: n.NodeID, Program: n.Program, Batch: i, State: state, DesiredSHA256: r.SHA256})
		}
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if e := seen.check(tx); e != nil {
			return e
		}
		scope, hash := "release-deployment-create:"+p.User.ID, result.RequestSHA256
		duplicate, e := tx.BusinessRequest(scope, in.RequestID, hash, &result)
		if e != nil || duplicate {
			return e
		}
		old, e := tx.Get("release_deployment", result.ID)
		if e == nil {
			prior, e := store.Decode[model.ReleaseDeployment](old)
			if e != nil {
				return e
			}
			if prior.RequestSHA256 != result.RequestSHA256 {
				return store.ErrConflict
			}
			result = prior
			return tx.CompleteBusinessRequest(scope, in.RequestID, hash, result)
		}
		if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if rollbackOf != "" {
			old, e := tx.Get("release_deployment", rollbackOf)
			if e != nil {
				return e
			}
			prior, e := store.Decode[model.ReleaseDeployment](old)
			if e != nil {
				return e
			}
			if prior.Version != expected {
				return store.ErrConflict
			}
			if prior.State != "completed" && prior.State != "failed" && prior.State != "cancelled" {
				return errors.New("pause and cancel an active deployment before selecting an old release")
			}
			for _, target := range prior.Targets {
				if target.Runtime != nil {
					if e = releasebundle.CheckDatabase(*releasebundle.Program(r.Manifest).Build, target.Runtime.MigrationVersion); e != nil {
						return e
					}
				}
			}
		}
		for i := range result.Targets {
			t := &result.Targets[i]
			n, e := s.Nodes.LookupTx(tx, t.IdentityID)
			if e != nil {
				return e
			}
			if n.Version != identities[t.IdentityID].Version || n.Generation != identities[t.IdentityID].Generation {
				return store.ErrConflict
			}
			if e = s.checkTarget(r, n); e != nil {
				return e
			}
			doc, e := tx.Get("release_assignment", t.IdentityID)
			a := model.ReleaseAssignment{IdentityID: t.IdentityID, Version: 1, Generation: 1}
			if e == nil {
				a, e = store.Decode[model.ReleaseAssignment](doc)
				if e != nil {
					return e
				}
				priorDoc, e := tx.Get("release_deployment", a.DeploymentID)
				if e != nil {
					return e
				}
				prior, e := store.Decode[model.ReleaseDeployment](priorDoc)
				if e != nil {
					return e
				}
				if prior.State != "completed" && prior.State != "failed" && prior.State != "cancelled" {
					return fmt.Errorf("workload %s already has an active deployment", t.IdentityID)
				}
				for _, oldTarget := range prior.Targets {
					if oldTarget.IdentityID == t.IdentityID && oldTarget.Runtime != nil {
						if e = releasebundle.CheckDatabase(*releasebundle.Program(r.Manifest).Build, oldTarget.Runtime.MigrationVersion); e != nil {
							return e
						}
					}
				}
				a.Generation++
				a.Version = doc.Version + 1
			} else if !errors.Is(e, store.ErrNotFound) {
				return e
			}
			a.DeploymentID = result.ID
			t.Generation = a.Generation
			if _, e = tx.Put("release_assignment", t.IdentityID, doc.Version, a); e != nil {
				return e
			}
		}
		if _, e = tx.Put("release_deployment", result.ID, 0, result); e != nil {
			return e
		}
		if e = tx.Audit(p.Actor, "release.deployment.create", result.GroupID, result.ID, map[string]any{"release_id": r.ID, "sha256": r.SHA256, "batches": in.Batches, "reason": in.Reason, "rollback_of": rollbackOf}); e != nil {
			return e
		}
		return tx.CompleteBusinessRequest(scope, in.RequestID, hash, result)
	})
	if err == nil {
		s.actions(ctx, p, &result)
	}
	return result, err
}

func (s *Releases) Deployment(ctx context.Context, p identity.Principal, id string) (model.ReleaseDeployment, error) {
	doc, err := s.Store.Get(ctx, "release_deployment", id)
	if err != nil {
		return model.ReleaseDeployment{}, err
	}
	d, err := store.Decode[model.ReleaseDeployment](doc)
	if err != nil {
		return d, err
	}
	resources := []string{d.GroupID}
	for _, t := range d.Targets {
		resources = append(resources, t.NodeID)
	}
	seen, err := s.Business.authorize(ctx, p, "read", resources)
	if err != nil {
		return model.ReleaseDeployment{}, err
	}
	if _, err = s.Get(ctx, p, d.ReleaseID); err != nil {
		return model.ReleaseDeployment{}, err
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
	s.actions(ctx, p, &d)
	return d, err
}
func (s *Releases) Deployments(ctx context.Context, p identity.Principal) ([]model.ReleaseDeployment, error) {
	if _, err := s.Business.authorize(ctx, p, "read", nil); err != nil {
		return nil, err
	}
	docs, err := s.Store.List(ctx, "release_deployment")
	if err != nil {
		return nil, err
	}
	out := []model.ReleaseDeployment{}
	for _, doc := range docs {
		d, e := s.Deployment(ctx, p, doc.ID)
		if errors.Is(e, identity.ErrDenied) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *Releases) actions(ctx context.Context, p identity.Principal, d *model.ReleaseDeployment) {
	for i := range d.Targets {
		t := &d.Targets[i]
		t.ReportAgeMS = 0
		if t.LastReportMS > 0 {
			t.ReportAgeMS = max(0, s.Store.CurrentTime().UnixMilli()-t.LastReportMS)
		}
		ttl := s.OfflineAfterMS
		if ttl <= 0 {
			ttl = 15000
		}
		t.ReportFresh = t.LastReportMS > 0 && t.ReportAgeMS <= ttl
	}
	resources := []string{d.GroupID}
	for _, target := range d.Targets {
		resources = append(resources, target.NodeID)
	}
	seen, permissionErr := s.Business.authorize(ctx, p, "publish", resources)
	if permissionErr == nil {
		permissionErr = s.Store.Write(ctx, seen.check)
	}
	d.AllowedActions = []model.BusinessAction{}
	for _, action := range []string{"pause", "resume", "retry", "cancel", "rollback"} {
		allowed := action == "pause" && d.State == "active" || action == "resume" && d.State == "paused" || action == "retry" && d.State == "failed" || action == "cancel" && (d.State == "active" || d.State == "paused" || d.State == "failed") || action == "rollback" && (d.State == "completed" || d.State == "failed" || d.State == "cancelled")
		reason := ""
		if !allowed {
			reason = "the current deployment state does not allow this action"
		}
		if permissionErr != nil {
			allowed = false
			reason = "publish permission is required"
		}
		d.AllowedActions = append(d.AllowedActions, model.BusinessAction{Action: action, Allowed: allowed, Reason: reason})
	}
}

func (s *Releases) Action(ctx context.Context, p identity.Principal, id string, in ReleaseDeploymentActionInput) (model.ReleaseDeployment, error) {
	if err := businessID(in.RequestID); err != nil {
		return model.ReleaseDeployment{}, err
	}
	d, err := s.Deployment(ctx, p, id)
	if err != nil {
		return d, err
	}
	if err = businessReason(in.Reason); err != nil {
		return d, err
	}
	resources := []string{d.GroupID}
	for _, t := range d.Targets {
		resources = append(resources, t.NodeID)
	}
	seen, err := s.Business.authorize(ctx, p, "publish", resources)
	if err != nil {
		return d, err
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if e := seen.check(tx); e != nil {
			return e
		}
		scope := "release-action:" + id + ":" + p.User.ID
		hash := store.Hash(in)
		duplicate, e := tx.BusinessRequest(scope, in.RequestID, hash, &d)
		if e != nil || duplicate {
			return e
		}
		doc, e := tx.Get("release_deployment", id)
		if e != nil {
			return e
		}
		d, e = store.Decode[model.ReleaseDeployment](doc)
		if e != nil {
			return e
		}
		if d.Version != in.ExpectedVersion {
			return store.ErrConflict
		}
		switch in.Action {
		case "pause":
			if d.State != "active" {
				return errors.New("only active deployments can be paused")
			}
			d.State = "paused"
		case "resume":
			if d.State != "paused" {
				return errors.New("only paused deployments can be resumed")
			}
			d.State = "active"
		case "cancel":
			if d.State == "completed" || d.State == "cancelled" {
				return errors.New("deployment is already terminal")
			}
			d.State = "cancelled"
		case "retry":
			if d.State != "failed" {
				return errors.New("only failed deployments can be retried")
			}
			d.State = "active"
			d.Batches[d.CurrentBatch].State = "active"
			for i := range d.Targets {
				target := &d.Targets[i]
				if target.Batch != d.CurrentBatch || target.State != "failed" {
					continue
				}
				adoc, e := tx.Get("release_assignment", target.IdentityID)
				if e != nil {
					return e
				}
				a, e := store.Decode[model.ReleaseAssignment](adoc)
				if e != nil {
					return e
				}
				if a.DeploymentID != id || a.Generation != target.Generation {
					return store.ErrConflict
				}
				a.Generation++
				a.Version = adoc.Version + 1
				target.Generation = a.Generation
				target.State = "waiting"
				target.LastSequence = 0
				target.LastReportSHA256 = ""
				target.Reason = ""
				target.FailureComponent = ""
				target.PreparedSHA256 = ""
				target.AppliedSHA256 = ""
				target.RunningSHA256 = ""
				if _, e = tx.Put("release_assignment", a.IdentityID, adoc.Version, a); e != nil {
					return e
				}
			}
		default:
			return errors.New("unsupported release action")
		}
		d.Version++
		d.UpdatedMS = s.Store.CurrentTime().UnixMilli()
		d.Reason = in.Reason
		d.AllowedActions = nil
		if _, e = tx.Put("release_deployment", id, doc.Version, d); e != nil {
			return e
		}
		if e = tx.Audit(p.Actor, "release.deployment."+in.Action, d.GroupID, id, map[string]any{"reason": in.Reason, "version": d.Version}); e != nil {
			return e
		}
		return tx.CompleteBusinessRequest(scope, in.RequestID, hash, d)
	})
	if err == nil {
		s.actions(ctx, p, &d)
	}
	return d, err
}

func (s *Releases) Rollback(ctx context.Context, p identity.Principal, id string, in ReleaseRollbackInput) (model.ReleaseDeployment, error) {
	d, err := s.Deployment(ctx, p, id)
	if err != nil {
		return d, err
	}
	batches := [][]string{}
	for _, b := range d.Batches {
		batches = append(batches, append([]string{}, b.IdentityIDs...))
	}
	return s.createDeployment(ctx, p, CreateReleaseDeploymentInput{ID: in.ID, RequestID: in.RequestID, ReleaseID: in.ReleaseID, GroupID: d.GroupID, Batches: batches, Reason: in.Reason}, id, in.ExpectedVersion)
}

func decodeRelease(doc store.Document) (model.Release, error) {
	r, e := store.Decode[model.Release](doc)
	if e == nil && r.SHA256 != releasebundle.ManifestDigest(r.Manifest) {
		e = errors.New("stored release content digest mismatch")
	}
	return r, e
}
func actorForWorkload(p nodeidentity.Principal) model.Actor {
	return model.Actor{UserID: "workload:" + p.Identity.ID, Name: p.Identity.NodeID, Source: "authenticated-workload", SessionID: p.InstanceID}
}

// Reports are immutable. Replaying the same key/content has no new audit or
// business effect; every new report is checked against the current assignment.
func (s *Releases) Report(ctx context.Context, p nodeidentity.Principal, in model.ReleaseNodeReport) (model.ReleaseTarget, error) {
	var result model.ReleaseTarget
	if in.IdentityID != p.Identity.ID || in.NodeID != p.Identity.NodeID || in.Sequence < 1 || in.Generation < 1 {
		return result, identity.ErrDenied
	}
	if !slices.Contains([]string{"prepared", "applied", "running", "failed"}, in.State) {
		return result, errors.New("unknown release report state")
	}
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		if _, e := s.Nodes.ValidateTx(tx, p, "release"); e != nil {
			return e
		}
		doc, e := tx.Get("release_deployment", in.DeploymentID)
		if e != nil {
			return e
		}
		d, e := store.Decode[model.ReleaseDeployment](doc)
		if e != nil {
			return e
		}
		oldBusiness := releaseBusinessHash(d)
		adoc, e := tx.Get("release_assignment", p.Identity.ID)
		if e != nil {
			return e
		}
		a, e := store.Decode[model.ReleaseAssignment](adoc)
		if e != nil {
			return e
		}
		if a.DeploymentID != d.ID || a.Generation != in.Generation || d.ReleaseSHA256 != in.ReleaseSHA256 {
			return fmt.Errorf("%w: release assignment has changed", store.ErrConflict)
		}
		index := -1
		for i, t := range d.Targets {
			if t.IdentityID == p.Identity.ID {
				index = i
				break
			}
		}
		if index < 0 {
			return identity.ErrDenied
		}
		target := &d.Targets[index]
		if target.Generation != in.Generation || target.Batch > d.CurrentBatch {
			return fmt.Errorf("%w: batch is not active", store.ErrConflict)
		}
		hash := store.Hash(in)
		var oldHash string
		e = tx.QueryRowContext(ctx, "SELECT payload_hash FROM sf_release_reports WHERE identity_id=$1 AND deployment_id=$2 AND generation=$3 AND instance_epoch=$4 AND sequence=$5", p.Identity.ID, d.ID, in.Generation, p.InstanceEpoch, in.Sequence).Scan(&oldHash)
		if e == nil {
			if oldHash != hash {
				return store.ErrConflict
			}
			result = *target
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if d.State == "cancelled" || d.State == "paused" || d.State == "failed" {
			return fmt.Errorf("%w: deployment is %s", store.ErrConflict, d.State)
		}
		if target.AgentInstanceEpoch > p.InstanceEpoch || target.AgentInstanceEpoch == p.InstanceEpoch && in.Sequence <= target.LastSequence {
			return fmt.Errorf("%w: stale report sequence", store.ErrConflict)
		}
		rdoc, e := tx.Get("release", d.ReleaseID)
		if e != nil {
			return e
		}
		r, e := decodeRelease(rdoc)
		if e != nil {
			return e
		}
		if in.State == "prepared" {
			if target.State == "applied" || target.State == "running" {
				if target.AgentInstanceEpoch == p.InstanceEpoch {
					return errors.New("report would regress applied release state")
				}
			}
			target.PreparedSHA256 = r.SHA256
		}
		if in.State == "applied" || in.State == "running" {
			if target.PreparedSHA256 != r.SHA256 || target.AgentInstanceEpoch != p.InstanceEpoch {
				return errors.New("release must be prepared before applying")
			}
			if in.Runtime == nil {
				return errors.New("application report requires the actual process runtime")
			}
			if e = verifyReleaseRuntime(r, *target, *in.Runtime); e != nil {
				return e
			}
			target.AppliedSHA256 = r.SHA256
			target.Runtime = in.Runtime
			if in.State == "running" {
				target.RunningSHA256 = r.SHA256
			}
		}
		if in.State == "failed" {
			if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 4000 {
				return errors.New("failed report requires a bounded reason")
			}
			if in.ComponentID != "" {
				found := false
				for _, c := range r.Manifest.Components {
					found = found || c.ID == in.ComponentID
				}
				if !found {
					return errors.New("failed component is not in this release")
				}
			}
			target.FailureComponent = in.ComponentID
			target.Reason = in.Reason
			if d.State != "completed" {
				d.State = "failed"
				d.Batches[target.Batch].State = "failed"
			}
		}
		target.State = in.State
		target.AgentInstanceID = p.InstanceID
		target.AgentInstanceEpoch = p.InstanceEpoch
		target.LastSequence = in.Sequence
		target.LastReportSHA256 = hash
		target.LastSeenMS = s.Store.CurrentTime().UnixMilli()
		if in.State == "applied" || in.State == "running" {
			target.LastReportMS = target.LastSeenMS
			target.FailureComponent = ""
			target.Reason = ""
		}
		if releaseBusinessHash(d) != oldBusiness {
			d.Version++
		}
		d.UpdatedMS = target.LastSeenMS
		d.AllowedActions = nil
		raw, e := json.Marshal(in)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO sf_release_reports(identity_id,deployment_id,generation,instance_epoch,sequence,payload_hash,data,received_ms) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", p.Identity.ID, d.ID, in.Generation, p.InstanceEpoch, in.Sequence, hash, string(raw), target.LastSeenMS); e != nil {
			return e
		}
		if _, e = tx.Put("release_deployment", d.ID, doc.Version, d); e != nil {
			return e
		}
		result = *target
		return tx.Audit(actorForWorkload(p), "release.node."+in.State, target.NodeID, d.ID, map[string]any{"generation": in.Generation, "sequence": in.Sequence, "release_sha256": r.SHA256, "report_sha256": hash, "component_id": in.ComponentID, "reason": in.Reason, "process_instance_id": processInstance(in.Runtime)})
	})
	return result, err
}

func processInstance(r *model.ReleaseRuntime) string {
	if r == nil {
		return ""
	}
	return r.ProcessInstanceID
}

func verifyReleaseRuntime(r model.Release, t model.ReleaseTarget, run model.ReleaseRuntime) error {
	program := releasebundle.Program(r.Manifest)
	if !run.Healthy || run.NodeID != t.NodeID || run.Program != t.Program || run.ProcessInstanceID == "" || run.PID <= 0 || run.StartedMS <= 0 || run.ReleaseID != r.ID || run.ReleaseSHA256 != r.SHA256 || run.ProgramSHA256 != program.SHA256 || store.Hash(run.Build) != store.Hash(program.Build) {
		return errors.New("actual process identity, build or release digest does not match the target")
	}
	if e := releasebundle.CheckDatabase(run.Build, run.MigrationVersion); e != nil {
		return e
	}
	if !releasebundle.ValidDigest(run.PolicySHA256) {
		return errors.New("actual policy digest is required")
	}
	seen := map[string]bool{}
	for _, actual := range run.Components {
		if seen[actual.ID] {
			return errors.New("runtime component is repeated")
		}
		seen[actual.ID] = true
		matched := false
		for _, c := range releasebundle.ComponentsForNode(r.Manifest, t.NodeID) {
			if c.ID == actual.ID && c.Kind == actual.Kind && c.Version == actual.Version && c.SHA256 == actual.SHA256 && releasebundle.ValidDigest(actual.AppliedSHA256) {
				if c.Kind == "program" && actual.AppliedSHA256 != c.SHA256 || c.Kind == "configuration" && (actual.AppliedSHA256 != c.SHA256 || actual.AppliedVersion != c.Configuration.Version) || c.Kind == "rule" && actual.AppliedVersion < 1 {
					return fmt.Errorf("runtime component %s has inconsistent applied content", c.ID)
				}
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("runtime component %s differs from the release", actual.ID)
		}
	}
	if len(seen) != len(releasebundle.ComponentsForNode(r.Manifest, t.NodeID)) {
		return errors.New("actual runtime report is missing components")
	}
	return nil
}

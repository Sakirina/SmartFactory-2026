package application

import (
	"context"
	"errors"
	"slices"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type ReleaseConfigurationAuthorization struct {
	DeploymentID string                         `json:"deployment_id"`
	Generation   int64                          `json:"generation"`
	References   []model.ConfigurationReference `json:"references"`
}
type ReleaseConfigurationGrant struct {
	Identity     model.WorkloadIdentity         `json:"identity"`
	DeploymentID string                         `json:"deployment_id"`
	Generation   int64                          `json:"generation"`
	References   []model.ConfigurationReference `json:"references"`
}

func (s *Business) AuthorizeConfigurationUser(ctx context.Context, p identity.Principal, action string, resources []string) error {
	if !slices.Contains([]string{"read", "config", "identity", "publish"}, action) {
		return identity.ErrDenied
	}
	seen, err := s.authorize(ctx, p, action, resources)
	if err != nil {
		return err
	}
	return s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
}

func (s *Releases) AuthorizeConfigurationTarget(ctx context.Context, p nodeidentity.Principal, in ReleaseConfigurationAuthorization) (ReleaseConfigurationGrant, error) {
	var result ReleaseConfigurationGrant
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		n, err := s.Nodes.ValidateTx(tx, p, "release")
		if err != nil {
			return err
		}
		doc, err := tx.Get("release_deployment", in.DeploymentID)
		if err != nil {
			return err
		}
		d, err := store.Decode[model.ReleaseDeployment](doc)
		if err != nil {
			return err
		}
		adoc, err := tx.Get("release_assignment", p.Identity.ID)
		if err != nil {
			return err
		}
		a, err := store.Decode[model.ReleaseAssignment](adoc)
		if err != nil {
			return err
		}
		if a.DeploymentID != d.ID || a.Generation != in.Generation {
			return store.ErrConflict
		}
		if d.State == "paused" || d.State == "failed" || d.State == "cancelled" {
			return store.ErrConflict
		}
		found := false
		for _, t := range d.Targets {
			if t.IdentityID == n.ID && t.Generation == a.Generation && t.Batch <= d.CurrentBatch {
				found = true
			}
		}
		if !found {
			return identity.ErrDenied
		}
		rdoc, err := tx.Get("release", d.ReleaseID)
		if err != nil {
			return err
		}
		r, err := decodeRelease(rdoc)
		if err != nil {
			return err
		}
		refs := releasebundle.ReferencesForNode(r.Manifest, n.NodeID)
		if len(refs) != len(in.References) {
			return errors.New("configuration target must contain the complete release reference set")
		}
		seen := map[model.ConfigurationReference]bool{}
		for _, ref := range refs {
			seen[ref] = true
		}
		for _, ref := range in.References {
			if !seen[ref] {
				return store.ErrConflict
			}
			delete(seen, ref)
		}
		if len(seen) != 0 {
			return store.ErrConflict
		}
		result = ReleaseConfigurationGrant{Identity: n, DeploymentID: d.ID, Generation: a.Generation, References: refs}
		return nil
	})
	return result, err
}

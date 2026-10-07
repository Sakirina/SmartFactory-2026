package application

import (
	"context"
	"errors"

	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type NodeConfiguration struct {
	Store    *store.Store
	Identity *identity.Manager
	Nodes    *nodeidentity.Service
	Config   *configcenter.Service
}

type SaveWorkloadIdentityInput struct {
	Identity        model.WorkloadIdentity `json:"identity" required:"true"`
	ExpectedVersion int64                  `json:"expected_version" required:"true" minimum:"0"`
}

type RotateWorkloadCredentialInput struct {
	Credential      string `json:"credential" required:"true" minLength:"32" maxLength:"512" writeOnly:"true"`
	ExpectedVersion int64  `json:"expected_version" required:"true" minimum:"1"`
}

func (s *NodeConfiguration) accessBusiness() *Business {
	return &Business{Store: s.Store, Identity: s.Identity}
}

func (s *NodeConfiguration) SaveIdentity(ctx context.Context, p identity.Principal, in SaveWorkloadIdentityInput) (model.WorkloadIdentity, error) {
	if s.Nodes.AuthorityURL != "" {
		return model.WorkloadIdentity{}, identity.ErrDenied
	}
	resources := []string{in.Identity.NodeID}
	if old, err := s.Nodes.Lookup(ctx, in.Identity.ID); err == nil {
		resources = append(resources, old.NodeID)
	} else if !errors.Is(err, store.ErrNotFound) {
		return model.WorkloadIdentity{}, err
	}
	seen, err := s.accessBusiness().authorize(ctx, p, "identity", resources)
	if err != nil {
		return model.WorkloadIdentity{}, err
	}
	var out model.WorkloadIdentity
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if e := seen.check(tx); e != nil {
			return e
		}
		var e error
		out, e = s.Nodes.PutTx(tx, p.Actor, in.Identity, in.ExpectedVersion)
		return e
	})
	return out, err
}

func (s *NodeConfiguration) RotateCredential(ctx context.Context, p identity.Principal, id string, in RotateWorkloadCredentialInput) (model.WorkloadIdentity, error) {
	if s.Nodes.AuthorityURL != "" {
		return model.WorkloadIdentity{}, identity.ErrDenied
	}
	w, err := s.Nodes.Lookup(ctx, id)
	if err != nil {
		return w, err
	}
	seen, err := s.accessBusiness().authorize(ctx, p, "identity", []string{w.NodeID})
	if err != nil {
		return w, err
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if e := seen.check(tx); e != nil {
			return e
		}
		var e error
		w, e = s.Nodes.RotateTx(tx, p.Actor, id, in.Credential, in.ExpectedVersion)
		return e
	})
	return w, err
}

func (s *NodeConfiguration) Identities(ctx context.Context, p identity.Principal) ([]model.WorkloadIdentity, error) {
	if _, err := s.accessBusiness().authorize(ctx, p, "read", nil); err != nil {
		return nil, err
	}
	docs, err := s.Store.List(ctx, "workload_identity")
	if err != nil {
		return nil, err
	}
	out := []model.WorkloadIdentity{}
	for _, d := range docs {
		w, e := store.Decode[model.WorkloadIdentity](d)
		if e != nil {
			return nil, e
		}
		seen, e := s.accessBusiness().authorize(ctx, p, "read", []string{w.NodeID})
		if errors.Is(e, identity.ErrDenied) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if e = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) }); e != nil {
			return nil, e
		}
		out = append(out, w)
	}
	return out, nil
}

func (s *NodeConfiguration) Reports(ctx context.Context, p identity.Principal) ([]model.ConfigurationReport, error) {
	if _, err := s.accessBusiness().authorize(ctx, p, "read", nil); err != nil {
		return nil, err
	}
	docs, err := s.Store.List(ctx, "configuration_report")
	if err != nil {
		return nil, err
	}
	out := []model.ConfigurationReport{}
	for _, d := range docs {
		r, e := store.Decode[model.ConfigurationReport](d)
		if e != nil {
			return nil, e
		}
		seen, e := s.accessBusiness().authorize(ctx, p, "read", []string{r.NodeID})
		if errors.Is(e, identity.ErrDenied) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if e = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) }); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}

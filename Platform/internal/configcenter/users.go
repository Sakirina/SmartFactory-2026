package configcenter

import (
	"context"
	"errors"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *Service) PutAsUser(ctx context.Context, token string, actor model.Actor, p Parameter, expected int64) (Parameter, error) {
	if s.workloadService().AuthorityURL == "" {
		return s.Put(ctx, actor, p, expected)
	}
	resources := append([]string{}, p.TargetNodeIDs...)
	if d, e := s.Store.Get(ctx, "parameter", p.ID); e == nil {
		old, e := store.Decode[Parameter](d)
		if e != nil {
			return p, e
		}
		resources = append(resources, old.TargetNodeIDs...)
	} else if !errors.Is(e, store.ErrNotFound) {
		return p, e
	}
	requestService := &Service{Store: s.Store, Identity: s.Identity, Workloads: s.Workloads, LegacySubscription: s.LegacySubscription}
	requestService.AuthorizeWrite = func(ctx context.Context) error {
		_, e := s.workloadService().AuthorizeUser(ctx, token, "config", resources)
		return e
	}
	return requestService.Put(ctx, actor, p, expected)
}

func (s *Service) PublicReports(ctx context.Context, token string) ([]model.ConfigurationReport, error) {
	service := s.workloadService()
	if service.AuthorityURL == "" {
		return nil, identity.ErrDenied
	}
	if _, err := service.AuthorizeUser(ctx, token, "read", nil); err != nil {
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
		if _, e = service.AuthorizeUser(ctx, token, "read", []string{r.NodeID}); errors.Is(e, identity.ErrDenied) {
			continue
		} else if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}

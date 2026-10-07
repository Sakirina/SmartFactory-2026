package application

import (
	"context"
	"errors"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// captureAuthorization pins user and grant state before Permit reads it. The
// grant collection also records membership, so additions and removals conflict.
type authorizationReader struct {
	Store    DocumentQueries
	Identity *identity.Manager
}

func (s *Definitions) captureAuthorization(ctx context.Context, p identity.Principal, seen *revisions) error {
	return (&authorizationReader{Store: s.Store, Identity: s.Identity}).captureAuthorization(ctx, p, seen)
}

func (s *authorizationReader) captureAuthorization(ctx context.Context, p identity.Principal, seen *revisions) error {
	if p.SessionDocument != "" {
		session, err := s.Store.Get(ctx, "session", p.SessionDocument)
		if err != nil {
			return identity.ErrAuthentication
		}
		if session.Version != p.SessionVersion {
			return store.ErrConflict
		}
		if err = seen.remember("session", session.ID, session.Version); err != nil {
			return err
		}
	}
	doc, err := s.Store.Get(ctx, "user", p.User.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if doc.Version != p.User.Version {
		return store.ErrConflict
	}
	if err := seen.remember("user", p.User.ID, doc.Version); err != nil {
		return err
	}
	grants, err := s.Store.List(ctx, "grant")
	if err != nil {
		return err
	}
	seen.grants = make(map[string]int64, len(grants))
	for _, grant := range grants {
		seen.grants[grant.ID] = grant.Version
		if err := seen.remember("grant", grant.ID, grant.Version); err != nil {
			return err
		}
	}
	return nil
}

// permit captures the same bounded entity ancestry that Identity.Permit may
// traverse. The actual authorization still belongs to Identity.Manager.
func (s *Definitions) permit(ctx context.Context, p identity.Principal, action, resource string, seen *revisions) error {
	return (&authorizationReader{Store: s.Store, Identity: s.Identity}).permit(ctx, p, action, resource, seen)
}

func (s *authorizationReader) permit(ctx context.Context, p identity.Principal, action, resource string, seen *revisions) error {
	for id, depth := resource, 0; id != "" && depth < 64; depth++ {
		doc, err := s.Store.Get(ctx, "entity", id)
		if errors.Is(err, store.ErrNotFound) {
			if err := seen.remember("entity", id, 0); err != nil {
				return err
			}
			break
		}
		if err != nil {
			return err
		}
		if err := seen.remember("entity", id, doc.Version); err != nil {
			return err
		}
		entity, err := store.Decode[model.Entity](doc)
		if err != nil {
			return err
		}
		id = entity.ParentID
	}
	return s.Identity.Permit(ctx, p, action, resource)
}

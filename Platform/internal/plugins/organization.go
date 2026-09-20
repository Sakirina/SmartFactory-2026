package plugins

import (
	"context"
	"errors"
	"fmt"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Department struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ParentID  string `json:"parent_id"`
	ManagerID string `json:"manager_id"`
}
type Member struct {
	ID           string `json:"id"`
	Login        string `json:"login"`
	Name         string `json:"name"`
	DepartmentID string `json:"department_id"`
	ManagerID    string `json:"manager_id"`
	Grade        string `json:"grade"`
	Email        string `json:"email"`
	Phone        string `json:"phone"`
	Active       bool   `json:"active"`
}
type OrganizationSync struct {
	ID          string       `json:"id"`
	Source      string       `json:"source"`
	Sequence    int64        `json:"sequence"`
	Full        bool         `json:"full"`
	Departments []Department `json:"departments"`
	Members     []Member     `json:"members"`
}

func SyncOrganization(ctx context.Context, s *store.Store, actor model.Actor, b OrganizationSync) error {
	if b.ID == "" || b.Source == "" || b.Sequence < 1 {
		return errors.New("sync requires id, source and positive sequence")
	}
	existing, e := s.List(ctx, "user")
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	departments := map[string]Department{}
	for _, d := range b.Departments {
		if d.ID == "" || d.Name == "" {
			return errors.New("department id and name are required")
		}
		departments[d.ID] = d
	}
	for _, d := range departments {
		visited := map[string]bool{}
		for id := d.ID; id != ""; {
			if visited[id] {
				return errors.New("organization hierarchy contains a cycle")
			}
			visited[id] = true
			next, ok := departments[id]
			if !ok {
				break
			}
			id = next.ParentID
		}
	}
	return s.Write(ctx, func(t *store.Tx) error {
		duplicate, e := t.Inbox("organization:"+b.ID, store.Hash(b), b.Source)
		if e != nil {
			return e
		}
		if duplicate {
			return nil
		}
		if d, e := t.Get("organization_cursor", b.Source); e == nil {
			var old struct {
				Sequence int64 `json:"sequence"`
			}
			if e = store.DecodeJSON(d.Data, &old); e != nil {
				return e
			}
			if b.Sequence <= old.Sequence {
				return store.ErrConflict
			}
		}
		for _, dep := range b.Departments {
			if _, e := t.Put("department", dep.ID, -1, dep); e != nil {
				return e
			}
		}
		for _, m := range b.Members {
			if m.ID == "" || m.Name == "" || m.Login == "" {
				return errors.New("member id, name and login are required")
			}
			seen[m.ID] = true
			u := model.User{ID: m.ID, Roles: []string{"viewer"}, Resources: []string{}, Teams: []string{}}
			if d, e := t.Get("user", m.ID); e == nil {
				if e = store.DecodeJSON(d.Data, &u); e != nil {
					return e
				}
			}
			u.Name = m.Name
			u.Login = m.Login
			u.DepartmentID = m.DepartmentID
			u.ManagerID = m.ManagerID
			u.Grade = m.Grade
			u.Email = m.Email
			u.Phone = m.Phone
			u.Active = m.Active
			u.Version++
			if _, e := t.Put("user", u.ID, -1, u); e != nil {
				return e
			}
			if _, e := t.Put("organization_owner", u.ID, -1, map[string]any{"source": b.Source}); e != nil {
				return e
			}
		}
		if b.Full {
			for _, d := range existing {
				u, e := store.Decode[model.User](d)
				if e != nil {
					return e
				}
				if seen[u.ID] {
					continue
				}
				owner, e := t.Get("organization_owner", u.ID)
				if errors.Is(e, store.ErrNotFound) {
					continue
				}
				if e != nil {
					return e
				}
				var v struct {
					Source string `json:"source"`
				}
				if e = store.DecodeJSON(owner.Data, &v); e != nil {
					return e
				}
				if v.Source == b.Source {
					u.Active = false
					u.Version++
					if _, e = t.Put("user", u.ID, -1, u); e != nil {
						return e
					}
				}
			}
		}
		if _, e := t.Put("organization_cursor", b.Source, -1, map[string]any{"sequence": b.Sequence, "sync_id": b.ID}); e != nil {
			return e
		}
		return t.Audit(actor, "organization.synchronize", b.Source, b.ID, map[string]any{"full": b.Full, "members": len(b.Members), "departments": len(b.Departments), "sequence": b.Sequence, "summary": fmt.Sprintf("%d members imported", len(b.Members))})
	})
}

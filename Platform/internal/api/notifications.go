package api

import (
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/notifications"
	"competition2026/product/platform/internal/store"
	"net/http"
)

func (s *Server) notifications(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	docs, err := s.Store.List(r.Context(), "notification")
	if err != nil {
		return err
	}
	out := []notifications.Delivery{}
	for _, doc := range docs {
		d, err := store.Decode[notifications.Delivery](doc)
		if err != nil {
			return err
		}
		if d.UserID != p.User.ID && !has(p.User.Roles, "admin") {
			continue
		}
		if s.allow(r, p, "read", d.EntityID) == nil {
			out = append(out, d)
		}
	}
	respond(w, 200, out)
	return nil
}

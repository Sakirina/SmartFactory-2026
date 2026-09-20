package api

import (
	"errors"
	"net/http"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/plugins"
	"competition2026/product/platform/internal/store"
)

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	docs, err := s.Store.List(r.Context(), "plugin")
	if err != nil {
		return err
	}
	result := []any{}
	for _, doc := range docs {
		spec, e := store.Decode[plugins.Spec](doc)
		if e != nil {
			return e
		}
		var state plugins.Status
		if saved, e := s.Store.Get(r.Context(), "plugin_status", spec.ID); e == nil {
			state, e = store.Decode[plugins.Status](saved)
			if e != nil {
				return e
			}
		}
		result = append(result, map[string]any{"spec": spec, "status": state})
	}
	respond(w, 200, result)
	return nil
}
func (s *Server) putPlugin(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.Mode != "cloud" {
		return errors.New("organization plugins run in the cloud")
	}
	var req struct {
		Plugin          plugins.Spec `json:"plugin"`
		ExpectedVersion int64        `json:"expected_version"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	manager := plugins.Manager{Store: s.Store, Config: s.Config}
	out, err := manager.Put(r.Context(), p.Actor, req.Plugin, req.ExpectedVersion)
	if err == nil {
		respond(w, 200, out)
	}
	return err
}

package api

import (
	"errors"
	"net/http"
	"strings"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *Server) dashboardAccess(r *http.Request, p identity.Principal, d model.Dashboard, action string) error {
	if d.GroupID == "" {
		return APIError{400, "dashboard group_id is required"}
	}
	if e := s.allow(r, p, action, d.GroupID); e != nil {
		return e
	}
	for _, id := range d.DeviceIDs {
		if id == "" {
			return APIError{400, "empty dashboard resource"}
		}
		if e := s.allow(r, p, "read", id); e != nil {
			return e
		}
	}
	for _, metric := range d.Metrics {
		if metric.DeviceID == "" || metric.Key == "" {
			return APIError{400, "metric requires device_id and key"}
		}
		if e := s.allow(r, p, "read", metric.DeviceID); e != nil {
			return e
		}
	}
	return nil
}

func (s *Server) dashboards(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	docs, e := s.Store.List(r.Context(), "dashboard")
	if e != nil {
		return e
	}
	result := []model.Dashboard{}
	for _, doc := range docs {
		d, e := store.Decode[model.Dashboard](doc)
		if e != nil {
			return e
		}
		if e := s.dashboardAccess(r, p, d, "read"); e != nil {
			if errors.Is(e, identity.ErrDenied) {
				continue
			}
			return e
		}
		result = append(result, d)
	}
	respond(w, 200, result)
	return nil
}

func (s *Server) putDashboard(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.Mode != "cloud" {
		return APIError{403, "dashboard changes are published in the cloud"}
	}
	var req struct {
		Dashboard       model.Dashboard `json:"dashboard"`
		ExpectedVersion int64           `json:"expected_version"`
	}
	if e := decode(r, &req); e != nil {
		return e
	}
	d := req.Dashboard
	if len(d.ID) > 128 || len(d.Title) > 200 || strings.TrimSpace(d.Title) == "" || req.ExpectedVersion < 0 {
		return APIError{400, "dashboard id, title or version is invalid"}
	}
	if len(d.DeviceIDs) > 20 || len(d.Keys) > 20 || len(d.Metrics) > 12 {
		return APIError{400, "dashboard supports 20 devices, 20 fields and 12 metric cards"}
	}
	if d.WindowMS < 1000 || d.WindowMS > 30*24*3600000 {
		return APIError{400, "dashboard window must be 1 second to 30 days"}
	}
	if d.RefreshMS != -1 && d.RefreshMS != 0 && (d.RefreshMS < 1000 || d.RefreshMS > 60000) {
		return APIError{400, "refresh_ms must be -1, 0 or 1000..60000"}
	}
	if e := s.dashboardAccess(r, p, d, "dashboard"); e != nil {
		return e
	}
	if d.ID == "" {
		d.ID = identity.ID()
	}
	if old, e := s.Store.Get(r.Context(), "dashboard", d.ID); e == nil {
		previous, e := store.Decode[model.Dashboard](old)
		if e != nil {
			return e
		}
		if e = s.dashboardAccess(r, p, previous, "dashboard"); e != nil {
			return e
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	d.Version = req.ExpectedVersion + 1
	e := s.Store.Write(r.Context(), func(tx *store.Tx) error {
		if _, e := tx.Put("dashboard", d.ID, req.ExpectedVersion, d); e != nil {
			return e
		}
		return tx.Audit(p.Actor, "dashboard.update", d.ID, "", d)
	})
	if e == nil {
		respond(w, 200, d)
	}
	return e
}

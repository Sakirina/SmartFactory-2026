package api

import (
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"errors"
	"net/http"
	"strings"
)

func (s *Server) createJob(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	var input struct {
		DeviceID string `json:"device_id"`
		FromMS   int64  `json:"from_ms"`
		ToMS     int64  `json:"to_ms"`
		Reason   string `json:"reason"`
	}
	if e := decode(r, &input); e != nil {
		return e
	}
	if input.DeviceID == "" || input.FromMS <= 0 || input.ToMS < input.FromMS || strings.TrimSpace(input.Reason) == "" {
		return errors.New("device_id, valid time range and reason required")
	}
	if e := s.allow(r, p, "register", input.DeviceID); e != nil {
		return e
	}
	if _, e := s.Store.Get(r.Context(), "entity", input.DeviceID); e != nil {
		return e
	}
	job := model.Job{ID: "manual:" + identity.ID(), Kind: "recompute", Status: "pending", DeviceID: input.DeviceID, FromMS: input.FromMS, ToMS: input.ToMS, Reason: input.Reason, Version: 1}
	e := s.Store.Write(r.Context(), func(t *store.Tx) error {
		if _, e := t.Put("job", job.ID, 0, job); e != nil {
			return e
		}
		return t.Audit(p.Actor, "recompute.request", job.DeviceID, job.ID, job)
	})
	if e == nil {
		respond(w, 202, job)
	}
	return e
}
func (s *Server) retryJob(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	job, e := s.TaskApplication().RetryJob(r.Context(), p, r.PathValue("id"))
	if e == nil {
		respond(w, 202, job)
	}
	return e
}

package api

import (
	"crypto/subtle"
	"fmt"
	"net/http"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *Server) nativeEvaluate(w http.ResponseWriter, r *http.Request) {
	if s.ServiceToken == "" || subtle.ConstantTimeCompare([]byte(bearer(r)), []byte(s.ServiceToken)) != 1 {
		respond(w, http.StatusUnauthorized, map[string]any{"committed": false, "error": "native service authentication required"})
		return
	}
	var req struct {
		Points      []model.Observation `json:"sf_observations"`
		Definitions []string            `json:"sf_definition_ids"`
		Committed   bool                `json:"committed"`
		Count       int                 `json:"count"`
		BatchID     string              `json:"sf_batch_id"`
	}
	if err := decode(r, &req); err != nil || len(req.Points) == 0 || len(req.Points) > 1000 {
		respond(w, 400, map[string]any{"committed": false, "error": "invalid native observation batch"})
		return
	}
	for _, point := range req.Points {
		stored, err := s.Store.FindObservation(r.Context(), point.ID, point.ObservedMS, point.DeviceID, point.Key)
		if err != nil || store.Hash(stored) != store.Hash(point) {
			respond(w, 409, map[string]any{"committed": false, "error": "observation does not match the committed ingress record"})
			return
		}
		historical := point.Late || s.Store.Now().UnixMilli()-point.ObservedMS > 15000
		if id := r.URL.Query().Get("definition_id"); id != "" {
			err = s.Engine.ProcessDefinition(r.Context(), id, stored, historical)
		} else {
			err = s.Engine.Process(r.Context(), stored, historical, "")
		}
		if err != nil {
			respond(w, 503, map[string]any{"committed": false, "error": err.Error()})
			return
		}
	}
	committed := true
	if id := r.URL.Query().Get("definition_id"); id != "" && req.BatchID != "" {
		found := false
		for _, target := range req.Definitions {
			found = found || target == id
		}
		if !found {
			respond(w, 400, map[string]any{"committed": false, "error": "definition not included in batch"})
			return
		}
		err := s.Store.Write(r.Context(), func(tx *store.Tx) error {
			expires := s.Store.Now().AddDate(0, 0, max(s.Store.Policy().Retention.RawDays, int(s.Store.Policy().AutoBackfillDays))).UnixMilli()
			if _, err := tx.InboxUntil("native-batch:"+req.BatchID+":"+id, store.Hash(req.Points), s.Store.NodeID, expires); err != nil {
				return err
			}
			for _, target := range req.Definitions {
				var count int
				if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM inbox WHERE id=$1", fmt.Sprintf("native-batch:%s:%s", req.BatchID, target)).Scan(&count); err != nil {
					return err
				}
				if count == 0 {
					committed = false
				}
			}
			return nil
		})
		if err != nil {
			respond(w, 503, map[string]any{"committed": false, "error": err.Error()})
			return
		}
	}
	respond(w, 200, map[string]any{"committed": committed, "count": len(req.Points), "sf_observations": req.Points, "sf_definition_ids": req.Definitions, "sf_batch_id": req.BatchID})
}

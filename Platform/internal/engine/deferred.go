package engine

import (
	"context"
	"errors"
	"fmt"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// A queue delay can turn initially timely ingress into historical evaluation.
// Retire the realtime delivery only after a recomputation request is committed.
func (s *Service) deferEvaluation(ctx context.Context, p model.Observation) error {
	// A delayed native projection can repeat a point already evaluated locally.
	docs, err := s.Store.List(ctx, "definition")
	if err != nil {
		return err
	}
	complete := true
	for _, doc := range docs {
		d, err := store.Decode[model.Definition](doc)
		if err != nil {
			return err
		}
		if d.Status != "published" || d.Kind == "strategy" || d.ID == p.DefinitionID {
			continue
		}
		matches, err := s.scope(ctx, d, p)
		if err != nil {
			return err
		}
		if !matches {
			continue
		}
		id := p.OriginID
		if id == "" {
			id = p.ID
		}
		var count int
		if err = s.Store.DB.QueryRowContext(ctx, "SELECT count(*) FROM inbox WHERE id=$1", fmt.Sprintf("evaluation:%s:%d:%s", d.ID, d.Version, id)).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			complete = false
			break
		}
	}
	if complete {
		return nil
	}
	return s.Store.Write(ctx, func(t *store.Tx) error {
		expires := s.Store.Now().AddDate(0, 0, max(s.Store.Policy().Retention.RawDays, int(s.Store.Policy().AutoBackfillDays))).UnixMilli()
		duplicate, e := t.InboxUntil("deferred-evaluation:"+p.ID, store.Hash(p), s.Store.NodeID, expires)
		if e != nil || duplicate {
			return e
		}
		job := model.Job{ID: "backfill:" + p.DeviceID, Kind: "recompute", Status: "pending", DeviceID: p.DeviceID, FromMS: p.ObservedMS, ToMS: p.ObservedMS, Reason: "evaluation exceeded realtime freshness", Version: 1}
		if p.DefinitionID != "" {
			job.DefinitionID = p.DefinitionID
		}
		if d, e := t.Get("job", job.ID); e == nil {
			old, e := store.Decode[model.Job](d)
			if e != nil {
				return e
			}
			if old.Status != "completed" {
				if old.FromMS < job.FromMS {
					job.FromMS = old.FromMS
				}
				if old.ToMS > job.ToMS {
					job.ToMS = old.ToMS
				}
				if old.DefinitionID != job.DefinitionID {
					job.DefinitionID = ""
				}
			}
			job.Version = d.Version + 1
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		_, e = t.Put("job", job.ID, -1, job)
		return e
	})
}

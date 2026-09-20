package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type historicalAtKey struct{}
type replayFilterKey struct{}
type replayStateInfo struct {
	LiveID       string `json:"live_id"`
	DefinitionID string `json:"definition_id"`
	Version      int64  `json:"version"`
	EntityID     string `json:"entity_id"`
	AtMS         int64  `json:"at_ms"`
}

// affected includes downstream definitions, and historical asset membership. The
// replay reads raw observations; derived observations are propagated synchronously.
func (s *Service) affected(ctx context.Context, job model.Job) (map[string]bool, []string, error) {
	docs, err := s.Store.List(ctx, "definition")
	if err != nil {
		return nil, nil, err
	}
	defs := []model.Definition{}
	selected := map[string]bool{}
	for _, doc := range docs {
		d, e := store.Decode[model.Definition](doc)
		if e != nil {
			return nil, nil, e
		}
		defs = append(defs, d)
		if job.DefinitionID != "" {
			selected[d.ID] = d.ID == job.DefinitionID
			continue
		}
		if has(d.Selector.DeviceIDs, job.DeviceID) {
			selected[d.ID] = true
		}
		if d.Selector.AssetID != "" {
			versions, e := s.Store.Versions(ctx, "entity", job.DeviceID)
			if e != nil {
				return nil, nil, e
			}
			for _, v := range versions {
				p := model.Observation{DeviceID: job.DeviceID, ObservedMS: v.UpdatedMS}
				if len(d.Selector.Keys) > 0 {
					p.Key = d.Selector.Keys[0]
				}
				match, e := s.scope(ctx, d, p)
				if e != nil {
					return nil, nil, e
				}
				if match {
					selected[d.ID] = true
					break
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, d := range defs {
			if selected[d.ID] {
				continue
			}
			for _, id := range d.Dependencies {
				if selected[id] {
					selected[d.ID] = true
					changed = true
					break
				}
			}
		}
	}
	ids := map[string]bool{job.DeviceID: true}
	all := false
	for _, d := range defs {
		if selected[d.ID] {
			for _, id := range d.Selector.DeviceIDs {
				ids[id] = true
			}
			if d.Selector.AssetID != "" {
				all = true
			}
		}
	}
	if all {
		return selected, nil, nil
	}
	list := []string{}
	for id := range ids {
		if id != "" {
			list = append(list, id)
		}
	}
	sort.Strings(list)
	return selected, list, nil
}
func (s *Service) rawRange(ctx context.Context, ids []string) (int64, int64, error) {
	return s.Store.ObservationRange(ctx, ids, true)
}
func (s *Service) Recompute(ctx context.Context, job model.Job) error {
	if job.FromMS <= 0 || job.ToMS < job.FromMS {
		return errors.New("invalid recomputation range")
	}
	selected, ids, err := s.affected(ctx, job)
	if err != nil {
		return err
	}
	start, end, err := s.rawRange(ctx, ids)
	if err != nil {
		return err
	}
	cutoff := s.Store.Now().AddDate(0, 0, -s.Store.Policy().Retention.RawDays).UnixMilli()
	if start < cutoff {
		start = cutoff
	}
	if job.FromMS < cutoff {
		job.Error = "raw history before retention is unavailable"
	}
	if end < job.ToMS {
		end = job.ToMS
	}
	job.Status = "running"
	job.Progress = 0
	doc, err := s.Store.Put(ctx, "job", job.ID, job.Version, job)
	if err != nil {
		return err
	}
	version := doc.Version
	runID := fmt.Sprintf("%s:%d", job.ID, version)
	ctx = context.WithValue(ctx, replayStartKey{}, start)
	ctx = context.WithValue(ctx, replayFilterKey{}, selected)
	if start > 0 {
		for cursor := start; cursor <= end; {
			last := cursor + int64(10*time.Minute/time.Millisecond) - 1
			if last > end {
				last = end
			}
			var result model.DataResult
			for {
				result, err = s.Store.Query(ctx, store.Query{DeviceIDs: ids, FromMS: cursor, ToMS: last, Limit: 40000, RawOnly: true})
				if err != nil {
					return err
				}
				if !result.Truncated {
					break
				}
				if last == cursor {
					return errors.New("more than 40000 raw observations share a timestamp")
				}
				last = cursor + (last-cursor)/2
			}
			sortReplay(result.Points)
			for index, point := range result.Points {
				if index%128 == 0 {
					current, err := s.Store.Get(ctx, "job", job.ID)
					if err != nil {
						return err
					}
					if current.Version != version {
						return nil
					}
				}
				if err = s.Process(ctx, point, true, runID); err != nil {
					return err
				}
			}
			cursor = last + 1
			job.CursorMS = last
			job.Progress = float64(last-start+1) / float64(end-start+1)
			doc, err = s.Store.Put(ctx, "job", job.ID, version, job)
			if errors.Is(err, store.ErrConflict) {
				return nil
			}
			if err != nil {
				return err
			}
			version = doc.Version
		}
	}
	// Serialize the short tail with live evaluation before replacing active states.
	// Ingress remains available and retains new observations in its durable outbox.
	s.mu.Lock()
	defer s.mu.Unlock()
	_, latest, err := s.rawRange(ctx, ids)
	if err != nil {
		return err
	}
	if latest > end {
		q, err := s.Store.Query(ctx, store.Query{DeviceIDs: ids, FromMS: end + 1, ToMS: latest, Limit: 40000, RawOnly: true})
		if err != nil {
			return err
		}
		if q.Truncated {
			return errors.New("recompute catch-up exceeds 40000 points; retry with the persisted raw history")
		}
		sortReplay(q.Points)
		for _, point := range q.Points {
			if err = s.replayLocked(ctx, point, runID, 0); err != nil {
				return err
			}
		}
		end = latest
	}
	job.ToMS = end
	job.CursorMS = end
	job.Status = "completed"
	job.Progress = 1
	job.Version = version + 1
	err = s.finishReplay(ctx, job, version, runID, start, selected)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	for from := job.FromMS / 60000 * 60000; from <= end; {
		to := from + int64(24*time.Hour/time.Millisecond)
		if to > end+1 {
			to = end + 1
		}
		if err = s.Store.BuildRollups(ctx, from, to); err != nil {
			return err
		}
		from = to
	}
	return nil
}
func sortReplay(points []model.Observation) {
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].ObservedMS != points[j].ObservedMS {
			return points[i].ObservedMS < points[j].ObservedMS
		}
		if points[i].SourceSequence != points[j].SourceSequence {
			return points[i].SourceSequence < points[j].SourceSequence
		}
		return points[i].ID < points[j].ID
	})
}
func (s *Service) replayLocked(ctx context.Context, p model.Observation, run string, depth int) error {
	if depth >= 64 {
		return errors.New("derived dependency depth exceeds 64")
	}
	outputs, err := s.processLocked(ctx, p, true, run)
	if err != nil {
		return err
	}
	for _, output := range outputs {
		if err = s.replayLocked(ctx, output, run, depth+1); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) finishReplay(ctx context.Context, job model.Job, expected int64, run string, start int64, selected map[string]bool) error {
	states, err := s.Store.List(ctx, "recompute_state_info")
	if err != nil {
		return err
	}
	alarmDocs, err := s.Store.List(ctx, "recompute_alarm")
	if err != nil {
		return err
	}
	existingDocs, err := s.Store.List(ctx, "alarm")
	if err != nil {
		return err
	}
	computed := map[string]model.Alarm{}
	old := map[string]model.Alarm{}
	for _, doc := range alarmDocs {
		if strings.HasPrefix(doc.ID, run+":") {
			a, e := store.Decode[model.Alarm](doc)
			if e != nil {
				return e
			}
			computed[a.ID] = a
		}
	}
	for _, doc := range existingDocs {
		a, e := store.Decode[model.Alarm](doc)
		if e != nil {
			return e
		}
		if selected[a.DefinitionID] && a.StartedMS >= start && a.StartedMS <= job.ToMS {
			old[a.ID] = a
		}
	}
	type stateUpdate struct {
		Info   replayStateInfo
		States map[string]RuntimeState
	}
	updates := []stateUpdate{}
	for _, doc := range states {
		if !strings.HasPrefix(doc.ID, run+":") {
			continue
		}
		info, e := store.Decode[replayStateInfo](doc)
		if e != nil {
			return e
		}
		d, e := s.Published(ctx, info.DefinitionID, 0)
		if e != nil {
			return e
		}
		if d.Version != info.Version || d.Status != "published" {
			continue
		}
		state, e := s.Store.Get(ctx, "recompute_state", doc.ID)
		if e != nil {
			return e
		}
		value, e := store.Decode[map[string]RuntimeState](state)
		if e != nil {
			return e
		}
		updates = append(updates, stateUpdate{info, value})
	}
	return s.Store.Write(ctx, func(t *store.Tx) error {
		if _, e := t.Put("job", job.ID, expected, job); e != nil {
			return e
		}
		for _, update := range updates {
			if e := t.SetEphemeral("engine_state", update.Info.LiveID, update.States); e != nil {
				return e
			}
		}
		latestAlarms := map[string]model.Alarm{}
		remember := func(alarm model.Alarm) {
			key := alarm.DefinitionID + ":" + alarm.EntityID
			previous := latestAlarms[key]
			if previous.ID == "" || alarm.StartedMS > previous.StartedMS {
				latestAlarms[key] = alarm
			}
		}
		for id, alarm := range computed {
			previous, existed := old[id]
			alarm.Acknowledged = previous.Acknowledged
			alarm.Historical = !alarm.Active
			before := previous
			before.Version = 0
			before.RevisionStatus = ""
			before.RevisionReason = ""
			after := alarm
			after.Version = 0
			after.RevisionStatus = ""
			after.RevisionReason = ""
			if existed && store.Hash(before) == store.Hash(after) {
				remember(previous)
				continue
			}
			alarm.Version = previous.Version + 1
			alarm.RevisionStatus = "recomputed"
			alarm.RevisionReason = job.Reason
			remember(alarm)
			if _, e := t.Put("alarm", id, -1, alarm); e != nil {
				return e
			}
			if e := t.Enqueue(fmt.Sprintf("tb-alarm:%s:%d", id, alarm.Version), "tb_alarm", alarm.EntityID, alarm); e != nil {
				return e
			}
			if existed {
				rev := model.Revision{ID: store.Hash([]any{job.ID, id, alarm.Version}), JobID: job.ID, DeviceID: alarm.EntityID, Key: "alarm:" + alarm.DefinitionID, AtMS: alarm.StartedMS, Before: previous, After: alarm, Reason: job.Reason, Version: alarm.Version}
				if _, e := t.Put("revision", rev.ID, 0, rev); e != nil {
					return e
				}
			}
			d, err := definitionInTx(t, alarm.DefinitionID)
			if err != nil {
				return err
			}
			channels := append([]string{}, d.Policy.Channels...)
			if len(channels) == 0 {
				channels = []string{"in_app"}
			}
			if d.Policy.LateNotification == "silent" {
				channels = nil
			} else if !alarm.Active && d.Policy.LateNotification != "regular" {
				channels = []string{"in_app"}
			}
			for _, channel := range channels {
				nid := fmt.Sprintf("notification:%s:%d:%s", id, alarm.Version, channel)
				if e := t.Enqueue(nid, "notification", channel, map[string]any{"id": nid, "alarm": alarm, "recipients": d.Policy.Recipients, "channel": channel, "status": "pending"}); e != nil {
					return e
				}
			}
		}
		for key, alarm := range latestAlarms {
			if e := t.SetEphemeral("active_alarm", key, alarm); e != nil {
				return e
			}
		}
		for id, previous := range old {
			if _, ok := computed[id]; ok {
				continue
			}
			if previous.RevisionStatus == "invalidated" {
				continue
			}
			updated := previous
			updated.Active = false
			updated.Historical = true
			updated.ClearedMS = s.Store.Now().UnixMilli()
			updated.Version++
			updated.RevisionStatus = "invalidated"
			updated.RevisionReason = job.Reason
			if _, e := t.Put("alarm", id, -1, updated); e != nil {
				return e
			}
			if e := t.Enqueue(fmt.Sprintf("tb-alarm:%s:%d", id, updated.Version), "tb_alarm", updated.EntityID, updated); e != nil {
				return e
			}
			activeID := updated.DefinitionID + ":" + updated.EntityID
			if doc, e := t.Get("active_alarm", activeID); e == nil {
				current, e := store.Decode[model.Alarm](doc)
				if e != nil {
					return e
				}
				if current.ID == id {
					if e = t.SetEphemeral("active_alarm", activeID, updated); e != nil {
						return e
					}
				}
			} else if !errors.Is(e, store.ErrNotFound) {
				return e
			}
			rev := model.Revision{ID: store.Hash([]any{job.ID, id, updated.Version}), JobID: job.ID, DeviceID: updated.EntityID, Key: "alarm:" + updated.DefinitionID, AtMS: updated.StartedMS, Before: previous, After: updated, Reason: "historical recomputation invalidated alarm", Version: updated.Version}
			if _, e := t.Put("revision", rev.ID, 0, rev); e != nil {
				return e
			}
		}
		return t.Audit(model.Actor{UserID: "recompute", Source: s.Store.NodeID}, "history.recomputed", job.DeviceID, job.ID, job)
	})
}
func definitionInTx(t *store.Tx, id string) (model.Definition, error) {
	d, e := t.Get("definition", id)
	if e != nil {
		return model.Definition{}, e
	}
	return store.Decode[model.Definition](d)
}

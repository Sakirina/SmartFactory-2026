package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"competition2026/product/platform/pkg/model"
)

type IngestBatch struct {
	MessageID   string              `json:"message_id"`
	PayloadHash string              `json:"payload_hash"`
	SourceID    string              `json:"source_id"`
	Critical    bool                `json:"critical"`
	Points      []model.Observation `json:"points"`
	Event       any                 `json:"event,omitempty"`
	Gaps        []model.DataGap     `json:"gaps,omitempty"`
}
type IngestResult struct {
	MessageID string `json:"message_id"`
	Duplicate bool   `json:"duplicate"`
	Committed bool   `json:"committed"`
	Count     int    `json:"count"`
	Late      int    `json:"late"`
}
type Query struct {
	DeviceIDs        []string
	Keys             []string
	FromMS           int64
	ToMS             int64
	Limit            int
	Resolution       string
	IncludeRevisions bool
	RawOnly          bool
}
type Delivery struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Destination string          `json:"destination"`
	Payload     json.RawMessage `json:"payload"`
	CreatedMS   int64           `json:"created_ms"`
	Attempts    int             `json:"attempts"`
	NextMS      int64           `json:"next_ms"`
	LastError   string          `json:"last_error"`
}

func (s *Store) Ingest(ctx context.Context, batch IngestBatch) (IngestResult, error) {
	results, err := s.IngestMessages(ctx, []IngestBatch{batch})
	if len(results) == 0 {
		return IngestResult{MessageID: batch.MessageID}, err
	}
	return results[0], err
}

// IngestMessages preserves each message identity while committing the complete
// received batch atomically. No result is committed when any message fails.
func (s *Store) IngestMessages(ctx context.Context, batches []IngestBatch) ([]IngestResult, error) {
	if len(batches) > 1000 {
		return nil, errors.New("batch exceeds 1000 messages")
	}
	results := make([]IngestResult, len(batches))
	points := 0
	for i, batch := range batches {
		results[i].MessageID = batch.MessageID
		points += len(batch.Points)
		if batch.MessageID == "" || batch.SourceID == "" || points > 10000 {
			return results, errors.New("message_id and source_id are required; batch limit is 10000 points")
		}
	}
	if len(batches) == 0 {
		return results, nil
	}
	err := s.Write(ctx, func(t *Tx) error {
		jobs := map[string]model.Job{}
		for i, batch := range batches {
			hash := batch.PayloadHash
			if hash == "" {
				hash = Hash(batch)
			}
			if err := t.ingestMessage(batch, hash, &results[i], jobs); err != nil {
				return err
			}
		}
		return t.mergeBackfillJobs(jobs)
	})
	for i := range results {
		results[i].Committed = err == nil
	}
	return results, err
}
func (t *Tx) ingestMessage(batch IngestBatch, hash string, r *IngestResult, jobs map[string]model.Job) error {
	s := t.Store
	expires := int64(0)
	if !batch.Critical {
		expires = s.Now().AddDate(0, 0, int(s.Policy().AutoBackfillDays)).UnixMilli()
	}
	dup, e := t.InboxUntil(batch.MessageID, hash, batch.SourceID, expires)
	if e != nil {
		return e
	}
	r.Duplicate = dup
	if dup {
		return nil
	}
	previous, e := t.latestTimes(batch.Points)
	if e != nil {
		return e
	}
	accepted := make([]model.Observation, 0, len(batch.Points))
	for i, p := range batch.Points {
		if p.DeviceID == "" || p.Key == "" || p.ObservedMS <= 0 {
			return errors.New("observation requires device_id, key and observed_ms")
		}
		if p.Quality != "GOOD" && p.Quality != "BAD" && p.Quality != "UNCERTAIN" {
			return errors.New("invalid quality")
		}
		p.MessageID = batch.MessageID
		p.SourceID = batch.SourceID
		p.ID = fmt.Sprintf("%s:%d", batch.MessageID, i)
		if p.OriginID == "" {
			p.OriginID = p.ID
		}
		p.ReceivedMS = s.Now().UnixMilli()
		if p.TimeSource == "" {
			p.TimeSource = "collector"
		}
		key := seriesKey{p.DeviceID, p.Key}
		previousMS := previous[key]
		p.Late = p.Late || p.ObservedMS < previousMS || p.ReceivedMS-p.ObservedMS > 15000
		p.Revision = 1
		if p.ObservedMS < s.Now().AddDate(0, 0, -min(s.Policy().Retention.RawDays, int(s.Policy().AutoBackfillDays))).UnixMilli() {
			_, e = t.Put("quarantine", p.ID, 0, map[string]any{"observation": p, "expires_ms": s.Now().AddDate(0, 0, s.Policy().Retention.QuarantineDays).UnixMilli(), "reason": "outside automatic backfill or raw retention window"})
			if e != nil {
				return e
			}
			continue
		}
		accepted = append(accepted, p)
		previous[key] = max(previousMS, p.ObservedMS)
		r.Count++
		if p.Late {
			r.Late++
			job := model.Job{ID: "backfill:" + p.DeviceID, Kind: "recompute", Status: "pending", DeviceID: p.DeviceID, FromMS: p.ObservedMS, ToMS: previousMS, Reason: "late observation", Version: 1}
			if job.ToMS < p.ObservedMS {
				job.ToMS = p.ObservedMS
			}
			if previous, exists := jobs[job.ID]; exists {
				job.FromMS = min(previous.FromMS, job.FromMS)
				job.ToMS = max(previous.ToMS, job.ToMS)
			}
			jobs[job.ID] = job
		}
	}
	if e = t.insertIngestPoints(accepted); e != nil {
		return e
	}
	if batch.Critical {
		if _, e = t.Put("event", batch.MessageID, 0, map[string]any{"source_id": batch.SourceID, "payload": batch.Event, "received_ms": s.Now().UnixMilli()}); e != nil {
			return e
		}
	}
	for i, gap := range batch.Gaps {
		if gap.DeviceID == "" || gap.Key == "" || gap.FromMS <= 0 || gap.ToMS < gap.FromMS || gap.Missing <= 0 || gap.Reason == "" {
			return errors.New("invalid data gap")
		}
		gap.ID = fmt.Sprintf("%s:gap:%d", batch.MessageID, i)
		raw, e := json.Marshal(gap)
		if e != nil {
			return e
		}
		if _, e = t.ExecContext(t.Ctx, "INSERT INTO data_gaps(id,device_id,key,from_ms,to_ms,data) VALUES($1,$2,$3,$4,$5,$6)", gap.ID, gap.DeviceID, gap.Key, gap.FromMS, gap.ToMS, string(raw)); e != nil {
			return e
		}
	}
	if s.ForwardObservations && batch.SourceID == s.NodeID {
		if e = t.Enqueue("upstream:"+batch.MessageID, "cloud_observation", batch.SourceID, batch); e != nil {
			return e
		}
	}
	return t.SetEphemeral("source", batch.SourceID, model.SourceState{ID: batch.SourceID, LastSeenMS: s.Now().UnixMilli(), Status: "online", Backfill: map[bool]string{true: "pending", false: "complete"}[r.Late > 0]})
}

// All late measurements for a device in one committed batch share one revision.
// A new revision invalidates an in-progress replay, which will restart with all
// newly committed inputs after the incoming backfill batch has settled.
func (t *Tx) mergeBackfillJobs(jobs map[string]model.Job) error {
	for id, job := range jobs {
		doc, err := t.Get("job", id)
		if err == nil {
			old, err := Decode[model.Job](doc)
			if err != nil {
				return err
			}
			if old.Status != "completed" {
				job.FromMS = min(old.FromMS, job.FromMS)
				job.ToMS = max(old.ToMS, job.ToMS)
			}
			job.Version = doc.Version + 1
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if _, err = t.Put("job", id, -1, job); err != nil {
			return err
		}
	}
	return nil
}
func (t *Tx) SetEphemeral(kind, id string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = t.ExecContext(t.Ctx, "INSERT INTO documents(kind,id,version,updated_ms,data) VALUES($1,$2,1,$3,$4) ON CONFLICT(kind,id) DO UPDATE SET version=documents.version+1,updated_ms=excluded.updated_ms,data=excluded.data WHERE documents.data<>excluded.data", kind, id, t.Store.Now().UnixMilli(), string(b))
	return e
}
func (t *Tx) InsertPoint(p model.Observation) error {
	if e := t.EnsureDay(p.ObservedMS); e != nil {
		return e
	}
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	_, e = t.ExecContext(t.Ctx, "INSERT INTO observations(id,observed_ms,message_id,device_id,key,received_ms,revision,quality,definition_id,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", p.ID, p.ObservedMS, p.MessageID, p.DeviceID, p.Key, p.ReceivedMS, p.Revision, p.Quality, p.DefinitionID, string(b))
	if e != nil {
		return e
	}
	_, e = t.ExecContext(t.Ctx, "INSERT INTO latest(device_id,key,observed_ms,received_ms,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT(device_id,key) DO UPDATE SET observed_ms=excluded.observed_ms,received_ms=excluded.received_ms,data=excluded.data WHERE excluded.observed_ms>latest.observed_ms OR (excluded.observed_ms=latest.observed_ms AND excluded.received_ms>=latest.received_ms)", p.DeviceID, p.Key, p.ObservedMS, p.ReceivedMS, string(b))
	return e
}
func (s *Store) Latest(ctx context.Context, device, key string) (model.Observation, error) {
	var p model.Observation
	var b string
	e := s.DB.QueryRowContext(ctx, "SELECT data FROM latest WHERE device_id=$1 AND key=$2", device, key).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if e == nil {
		e = DecodeJSON([]byte(b), &p)
	}
	return p, e
}
func (s *Store) Sources(ctx context.Context) ([]model.SourceState, error) {
	ds, e := s.List(ctx, "source")
	if e != nil {
		return nil, e
	}
	out := []model.SourceState{}
	for _, d := range ds {
		v, e := Decode[model.SourceState](d)
		if e != nil {
			return nil, e
		}
		if s.Now().UnixMilli()-v.LastSeenMS > s.Policy().OfflineMS {
			v.Status = "offline"
			v.Reason = fmt.Sprintf("heartbeat older than %d ms", s.Policy().OfflineMS)
		}
		out = append(out, v)
	}
	return out, nil
}
func appendIn(q *strings.Builder, args *[]any, col string, values []string) {
	if len(values) == 0 {
		return
	}
	q.WriteString(" AND " + col + " IN (")
	for i, v := range values {
		if i > 0 {
			q.WriteByte(',')
		}
		*args = append(*args, v)
		fmt.Fprintf(q, "$%d", len(*args))
	}
	q.WriteByte(')')
}
func (s *Store) Query(ctx context.Context, opts Query) (model.DataResult, error) {
	r := model.DataResult{Points: []model.Observation{}, Revisions: []model.Revision{}, Quality: model.QualitySummary{Completeness: "unknown"}}
	if opts.ToMS <= 0 {
		opts.ToMS = s.Now().UnixMilli()
	}
	if opts.Limit <= 0 {
		opts.Limit = 2000
	}
	if opts.Limit > 40000 {
		opts.Limit = 40000
	}
	if opts.FromMS > opts.ToMS {
		return r, errors.New("from_ms must precede to_ms")
	}
	var q strings.Builder
	args := []any{opts.FromMS, opts.ToMS}
	q.WriteString("SELECT o.data FROM observations o WHERE o.observed_ms >= $1 AND o.observed_ms <= $2")
	if opts.RawOnly {
		q.WriteString(" AND o.definition_id=''")
	} else {
		q.WriteString(` AND (o.definition_id='' OR NOT EXISTS (SELECT 1 FROM observations newer WHERE newer.definition_id<>'' AND newer.device_id=o.device_id AND newer.key=o.key AND newer.observed_ms=o.observed_ms AND (newer.revision>o.revision OR (newer.revision=o.revision AND newer.received_ms>o.received_ms) OR (newer.revision=o.revision AND newer.received_ms=o.received_ms AND newer.id>o.id))))`)
	}
	appendIn(&q, &args, "o.device_id", opts.DeviceIDs)
	appendIn(&q, &args, "o.key", opts.Keys)
	fmt.Fprintf(&q, " ORDER BY o.observed_ms DESC,o.device_id,o.key,o.id LIMIT %d", opts.Limit+1)
	if opts.Resolution != "" && opts.Resolution != "raw" {
		return s.QueryRollups(ctx, opts)
	}
	rows, e := s.DB.QueryContext(ctx, q.String(), args...)
	if e != nil {
		return r, e
	}
	for rows.Next() {
		var b string
		var p model.Observation
		if e = rows.Scan(&b); e != nil {
			rows.Close()
			return r, e
		}
		if e = DecodeJSON([]byte(b), &p); e != nil {
			rows.Close()
			return r, e
		}
		r.Points = append(r.Points, p)
		if p.ReceivedMS > r.DataVersion {
			r.DataVersion = p.ReceivedMS
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return r, e
	}
	r.Points, e = s.archiveQuery(ctx, opts, r.Points)
	if e != nil {
		return r, e
	}
	r.DataVersion = 0
	if len(r.Points) > opts.Limit {
		r.Truncated = true
		r.Points = r.Points[:opts.Limit]
	}
	for _, p := range r.Points {
		r.DataVersion = max(r.DataVersion, p.ReceivedMS)
		switch p.Quality {
		case "GOOD":
			r.Quality.Good++
		case "BAD":
			r.Quality.Bad++
			r.Quality.Excluded++
		default:
			r.Quality.Uncertain++
			r.Quality.Excluded++
		}
	}
	sort.Slice(r.Points, func(i, j int) bool { return r.Points[i].ObservedMS < r.Points[j].ObservedMS })
	r.Sources, e = s.Sources(ctx)
	if e != nil {
		return r, e
	}
	if opts.IncludeRevisions {
		ds, e := s.List(ctx, "revision")
		if e != nil {
			return r, e
		}
		for _, d := range ds {
			v, e := Decode[model.Revision](d)
			if e != nil {
				return r, e
			}
			if v.AtMS >= opts.FromMS && v.AtMS <= opts.ToMS && containsOrAll(opts.DeviceIDs, v.DeviceID) && containsOrAll(opts.Keys, v.Key) {
				r.Revisions = append(r.Revisions, v)
			}
		}
	}
	if e := s.queryGaps(ctx, opts, &r); e != nil {
		return r, e
	}
	return r, nil
}

func (s *Store) queryGaps(ctx context.Context, opts Query, result *model.DataResult) error {
	result.Gaps = []model.DataGap{}
	var q strings.Builder
	q.WriteString("SELECT data FROM data_gaps WHERE to_ms >= $1 AND from_ms <= $2")
	args := []any{opts.FromMS, opts.ToMS}
	appendIn(&q, &args, "device_id", opts.DeviceIDs)
	appendIn(&q, &args, "key", opts.Keys)
	q.WriteString(" ORDER BY from_ms,id LIMIT 2001")
	rows, err := s.DB.QueryContext(ctx, q.String(), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var missing int64
	countKnown := true
	for rows.Next() {
		var raw string
		var gap model.DataGap
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if err := DecodeJSON([]byte(raw), &gap); err != nil {
			return err
		}
		if len(result.Gaps) >= 2000 {
			result.Truncated = true
			countKnown = false
			break
		}
		result.Gaps = append(result.Gaps, gap)
		// A partially intersecting group reports its range; an exact count only
		// exists when the query includes the whole group.
		if gap.FromMS >= opts.FromMS && gap.ToMS <= opts.ToMS {
			missing += gap.Missing
		} else {
			countKnown = false
		}
	}
	if len(result.Gaps) > 0 {
		result.Quality.Completeness = "incomplete"
		if countKnown {
			result.Quality.Missing = &missing
		}
	}
	return rows.Err()
}
func containsOrAll(list []string, s string) bool {
	if len(list) == 0 {
		return true
	}
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
func (s *Store) Deliveries(ctx context.Context, kind string, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, e := s.DB.QueryContext(ctx, "SELECT id,kind,destination,payload,created_ms,attempts,next_ms,last_error FROM outbox WHERE kind=$1 AND next_ms<=$2 ORDER BY created_ms,id LIMIT $3", kind, s.Now().UnixMilli(), limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var b string
		if e = rows.Scan(&d.ID, &d.Kind, &d.Destination, &b, &d.CreatedMS, &d.Attempts, &d.NextMS, &d.LastError); e != nil {
			return nil, e
		}
		d.Payload = json.RawMessage(b)
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) DeliveryDone(ctx context.Context, id string) error {
	return s.CompleteDeliveries(ctx, []string{id}, nil)
}
func (s *Store) DeliveryFailed(ctx context.Context, id string, err error) error {
	return s.CompleteDeliveries(ctx, nil, map[string]error{id: err})
}

// CompleteDeliveries commits a worker batch in one transaction. A failed commit
// leaves every delivery available for retry with its existing identifier.
func (s *Store) CompleteDeliveries(ctx context.Context, completed []string, failed map[string]error) error {
	if len(completed) == 0 && len(failed) == 0 {
		return nil
	}
	return s.Write(ctx, func(t *Tx) error {
		for start := 0; start < len(completed); start += 500 {
			var q strings.Builder
			q.WriteString("DELETE FROM outbox WHERE 1=1")
			var args []any
			appendIn(&q, &args, "id", completed[start:min(start+500, len(completed))])
			if _, e := t.ExecContext(ctx, q.String(), args...); e != nil {
				return e
			}
		}
		for id, err := range failed {
			if err == nil {
				return errors.New("failed delivery requires an error")
			}
			if _, e := t.ExecContext(ctx, "UPDATE outbox SET attempts=attempts+1,next_ms=$1,last_error=$2 WHERE id=$3", s.Now().Add(5*time.Second).UnixMilli(), err.Error(), id); e != nil {
				return e
			}
		}
		return nil
	})
}

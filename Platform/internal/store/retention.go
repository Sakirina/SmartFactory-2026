package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"competition2026/product/platform/pkg/model"
	"competition2026/product/platform/pkg/precise"
)

type Retention struct {
	RawDays        int `json:"raw_days"`
	MinuteDays     int `json:"minute_days"`
	HourDays       int `json:"hour_days"`
	DayDays        int `json:"day_days"`
	AlarmDays      int `json:"alarm_days"`
	QuarantineDays int `json:"quarantine_days"`
}

func DefaultRetention() Retention { return Retention{30, 90, 365, 1095, 1095, 7} }

type Aggregate = precise.Aggregate

func Number(value any) (*big.Rat, bool)                    { return precise.Number(value) }
func AggregatePoints(points []model.Observation) Aggregate { return precise.AggregatePoints(points) }
func MergeAggregate(a, b Aggregate) Aggregate              { return precise.MergeAggregate(a, b) }

func (s *Store) BuildRollups(ctx context.Context, from, to int64) error {
	return s.BuildRollupsGuarded(ctx, from, to, nil)
}

// BuildRollupsGuarded applies the caller's generation check in every write
// transaction. Previously committed buckets remain available after a stop.
func (s *Store) BuildRollupsGuarded(ctx context.Context, from, to int64, guard func(*Tx) error) error {
	if to <= from || to-from > int64(24*time.Hour/time.Millisecond) {
		return errors.New("rollup job must cover at most one day")
	}
	from = from / 60000 * 60000
	to = (to + 59999) / 60000 * 60000
	// Each hour is processed separately, keeping memory proportional to a bucket.
	for start := from; start < to; start += int64(time.Hour / time.Millisecond) {
		end := start + int64(time.Hour/time.Millisecond)
		if end > to {
			end = to
		}
		type rollup struct {
			device, key, unit string
			bucket            int64
			state             Aggregate
		}
		buckets := map[string]*rollup{}
		series, e := s.observationSeries(ctx, start, end)
		if e != nil {
			return e
		}
		for _, pair := range series {
			e = s.walkSeries(ctx, pair, start, end, func(p model.Observation) error {
				bucket := p.ObservedMS / 60000 * 60000
				k := fmt.Sprintf("%s\x00%s\x00%d", p.DeviceID, p.Key, bucket)
				r := buckets[k]
				if r == nil {
					r = &rollup{p.DeviceID, p.Key, p.Unit, bucket, Aggregate{Sum: "0", Min: "0", Max: "0", Complete: "unknown"}}
					buckets[k] = r
				}
				r.state = MergeAggregate(r.state, AggregatePoints([]model.Observation{p}))
				return nil
			})
			if e != nil {
				return e
			}
		}
		e = s.Write(ctx, func(t *Tx) error {
			if guard != nil {
				if err := guard(t); err != nil {
					return err
				}
			}
			for _, r := range buckets {
				data, _ := json.Marshal(map[string]any{"aggregate": r.state, "unit": r.unit})
				if _, e := t.ExecContext(ctx, "INSERT INTO rollups(device_id,key,granularity,bucket_ms,data) VALUES($1,$2,'minute',$3,$4) ON CONFLICT(device_id,key,granularity,bucket_ms) DO UPDATE SET data=excluded.data", r.device, r.key, r.bucket, string(data)); e != nil {
					return e
				}
				if e := t.markQueryRollup(r.device, r.key, "minute", r.bucket, string(data)); e != nil {
					return e
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	return s.combineRollups(ctx, from, to, guard)
}
func (s *Store) combineRollups(ctx context.Context, from, to int64, guard func(*Tx) error) error {
	for _, pair := range []struct {
		source, target string
		duration       int64
	}{{"minute", "hour", 3600000}, {"hour", "day", 86400000}} {
		start := from / pair.duration * pair.duration
		end := (to + pair.duration - 1) / pair.duration * pair.duration
		rows, e := s.DB.QueryContext(ctx, "SELECT device_id,key,bucket_ms,data FROM rollups WHERE granularity=$1 AND bucket_ms >= $2 AND bucket_ms < $3 ORDER BY device_id,key,bucket_ms", pair.source, start, end)
		if e != nil {
			return e
		}
		type row struct {
			device, key string
			bucket      int64
			state       Aggregate
			unit        string
		}
		group := map[string]*row{}
		for rows.Next() {
			var dev, key, b string
			var ts int64
			if e = rows.Scan(&dev, &key, &ts, &b); e != nil {
				rows.Close()
				return e
			}
			var data struct {
				Aggregate Aggregate `json:"aggregate"`
				Unit      string    `json:"unit"`
			}
			if e = DecodeJSON([]byte(b), &data); e != nil {
				rows.Close()
				return e
			}
			bucket := ts / pair.duration * pair.duration
			k := fmt.Sprintf("%s\x00%s\x00%d", dev, key, bucket)
			v := group[k]
			if v == nil {
				v = &row{device: dev, key: key, bucket: bucket, state: Aggregate{Sum: "0", Min: "0", Max: "0", Complete: "unknown"}, unit: data.Unit}
				group[k] = v
			}
			v.state = MergeAggregate(v.state, data.Aggregate)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		e = s.Write(ctx, func(t *Tx) error {
			if guard != nil {
				if err := guard(t); err != nil {
					return err
				}
			}
			for _, r := range group {
				data, _ := json.Marshal(map[string]any{"aggregate": r.state, "unit": r.unit})
				if _, e := t.ExecContext(ctx, "INSERT INTO rollups(device_id,key,granularity,bucket_ms,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT(device_id,key,granularity,bucket_ms) DO UPDATE SET data=excluded.data", r.device, r.key, pair.target, r.bucket, string(data)); e != nil {
					return e
				}
				if e := t.markQueryRollup(r.device, r.key, pair.target, r.bucket, string(data)); e != nil {
					return e
				}
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) QueryRollups(ctx context.Context, opts Query) (model.DataResult, error) {
	r := model.DataResult{Points: []model.Observation{}, Revisions: []model.Revision{}, Quality: model.QualitySummary{Completeness: "unknown"}}
	if opts.Resolution != "minute" && opts.Resolution != "hour" && opts.Resolution != "day" {
		return r, errors.New("resolution must be raw, minute, hour or day")
	}
	var q strings.Builder
	args := []any{opts.FromMS, opts.ToMS, opts.Resolution}
	q.WriteString("SELECT device_id,key,bucket_ms,data FROM rollups WHERE bucket_ms >= $1 AND bucket_ms <= $2 AND granularity=$3")
	appendIn(&q, &args, "device_id", opts.DeviceIDs)
	appendIn(&q, &args, "key", opts.Keys)
	fmt.Fprintf(&q, " ORDER BY bucket_ms DESC LIMIT %d", opts.Limit+1)
	rows, e := s.DB.QueryContext(ctx, q.String(), args...)
	if e != nil {
		return r, e
	}
	for rows.Next() {
		var p model.Observation
		var b string
		if e = rows.Scan(&p.DeviceID, &p.Key, &p.ObservedMS, &b); e != nil {
			rows.Close()
			return r, e
		}
		var data struct {
			Aggregate Aggregate `json:"aggregate"`
			Unit      string    `json:"unit"`
		}
		if e = DecodeJSON([]byte(b), &data); e != nil {
			rows.Close()
			return r, e
		}
		p.Value = data.Aggregate
		p.Unit = data.Unit
		p.Quality = "GOOD"
		if data.Aggregate.Count == 0 {
			p.Quality = "UNCERTAIN"
		}
		p.TimeSource = "aggregate"
		r.Points = append(r.Points, p)
		r.Quality.Good += data.Aggregate.Count
		r.Quality.Excluded += data.Aggregate.Excluded
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return r, e
	}
	if len(r.Points) > opts.Limit {
		r.Truncated = true
		r.Points = r.Points[:opts.Limit]
	}
	r.Sources, e = s.Sources(ctx)
	if e == nil {
		e = s.queryGaps(ctx, opts, &r)
	}
	return r, e
}
func (s *Store) ApplyRetention(ctx context.Context, p Retention) (map[string]int64, error) {
	if p.RawDays < 1 || p.MinuteDays < p.RawDays || p.HourDays < p.MinuteDays || p.DayDays < p.HourDays || p.AlarmDays < 1 || p.QuarantineDays < 1 {
		return nil, errors.New("invalid retention periods")
	}
	counts := map[string]int64{}
	rawCut := s.Now().AddDate(0, 0, -p.RawDays).UnixMilli()
	archived, err := s.expireArchives(ctx, rawCut)
	counts["archived_raw"] = archived
	if err != nil {
		return counts, err
	}
	err = s.Write(ctx, func(t *Tx) error {
		for _, cutoff := range []struct {
			resolution string
			days       int
		}{{"raw", p.RawDays}, {"minute", p.MinuteDays}, {"hour", p.HourDays}, {"day", p.DayDays}} {
			if e := t.expireQueryRows(cutoff.resolution, s.Now().AddDate(0, 0, -cutoff.days).UnixMilli()); e != nil {
				return e
			}
		}
		if s.Driver == "pgx" {
			rows, e := t.QueryContext(ctx, "SELECT tablename FROM pg_tables WHERE schemaname=current_schema() AND tablename LIKE 'observations_%'")
			if e != nil {
				return e
			}
			names := []string{}
			for rows.Next() {
				var name string
				if e = rows.Scan(&name); e != nil {
					rows.Close()
					return e
				}
				date, e := time.Parse("20060102", strings.TrimPrefix(name, "observations_"))
				if e == nil && date.AddDate(0, 0, 1).UnixMilli() <= rawCut {
					names = append(names, name)
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, name := range names {
				if e = t.retirePartition(name); e != nil {
					return e
				}
				if _, e = t.ExecContext(ctx, "DROP TABLE IF EXISTS "+name); e != nil {
					return e
				}
			}
			counts["partitions"] = int64(len(names))
		}
		queries := []struct {
			name, q string
			args    []any
		}{{"raw", "DELETE FROM observations WHERE observed_ms<$1", []any{rawCut}}, {"minute", "DELETE FROM rollups WHERE granularity='minute' AND bucket_ms<$1", []any{s.Now().AddDate(0, 0, -p.MinuteDays).UnixMilli()}}, {"hour", "DELETE FROM rollups WHERE granularity='hour' AND bucket_ms<$1", []any{s.Now().AddDate(0, 0, -p.HourDays).UnixMilli()}}, {"day", "DELETE FROM rollups WHERE granularity='day' AND bucket_ms<$1", []any{s.Now().AddDate(0, 0, -p.DayDays).UnixMilli()}}}
		for _, q := range queries {
			r, e := t.ExecContext(ctx, q.q, q.args...)
			if e != nil {
				return e
			}
			counts[q.name], _ = r.RowsAffected()
		}
		// Zero expiry identifies business/control deduplication and legacy records.
		expired, e := t.ExecContext(ctx, "DELETE FROM inbox WHERE expires_ms>0 AND expires_ms<=$1", s.Now().UnixMilli())
		if e != nil {
			return e
		}
		counts["inbox"], _ = expired.RowsAffected()
		// Preserve one checkpoint before raw retention so stateful counters can
		// replay the oldest retained observation without losing their baseline.
		result, e := t.ExecContext(ctx, `DELETE FROM engine_checkpoints WHERE at_ms<$1 AND at_ms<(SELECT MAX(previous.at_ms) FROM engine_checkpoints previous WHERE previous.state_id=engine_checkpoints.state_id AND previous.at_ms<$1)`, rawCut)
		if e != nil {
			return e
		}
		counts["checkpoints"], _ = result.RowsAffected()
		rows, e := t.QueryContext(ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind IN ('alarm','quarantine')")
		if e != nil {
			return e
		}
		remove := []Document{}
		for rows.Next() {
			d, err := scanDocument(rows)
			if err != nil {
				rows.Close()
				return err
			}
			expired := false
			if d.Kind == "alarm" {
				alarm, err := Decode[model.Alarm](d)
				if err != nil {
					rows.Close()
					return err
				}
				expired = !alarm.Active && alarm.UpdatedMS < s.Now().AddDate(0, 0, -p.AlarmDays).UnixMilli()
			} else {
				var quarantine struct {
					ExpiresMS int64 `json:"expires_ms"`
				}
				if err := DecodeJSON(d.Data, &quarantine); err != nil {
					rows.Close()
					return err
				}
				expired = quarantine.ExpiresMS > 0 && quarantine.ExpiresMS <= s.Now().UnixMilli()
			}
			if expired {
				remove = append(remove, d)
			}
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		rows.Close()
		for _, d := range remove {
			current, err := t.Get(d.Kind, d.ID)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if current.Version != d.Version || string(current.Data) != string(d.Data) {
				continue
			}
			if e = t.Delete(d.Kind, d.ID); e != nil {
				return e
			}
			if _, e = t.ExecContext(ctx, "DELETE FROM document_versions WHERE kind=$1 AND id=$2", d.Kind, d.ID); e != nil {
				return e
			}
			counts[d.Kind]++
		}

		return t.Audit(model.Actor{UserID: "retention-worker", Source: s.NodeID}, "storage.retention", "storage", "", map[string]any{"policy": p, "removed": counts})
	})
	return counts, err
}

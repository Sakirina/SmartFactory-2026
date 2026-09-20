package store

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"competition2026/product/platform/pkg/model"
)

const archivePlainLimit = 8 << 20

type ArchivePolicy struct {
	Enabled      bool `json:"enabled"`
	HotHours     int  `json:"hot_hours"`
	BlockPoints  int  `json:"block_points"`
	BlocksPerRun int  `json:"blocks_per_run"`
}

func DefaultArchivePolicy() ArchivePolicy {
	return ArchivePolicy{Enabled: true, HotHours: 24, BlockPoints: 4096, BlocksPerRun: 16}
}

type ArchiveStats struct {
	Blocks          int64 `json:"blocks"`
	Points          int64 `json:"points"`
	PlainBytes      int64 `json:"plain_bytes"`
	CompressedBytes int64 `json:"compressed_bytes"`
}
type archiveBlock struct {
	ID, Device, Key, Hash                             string
	First, Last, RawFirst, RawLast, Count, PlainBytes int64
	Payload                                           []byte
}

func encodeArchive(points []model.Observation) (archiveBlock, error) {
	b := archiveBlock{}
	if len(points) == 0 {
		return b, errors.New("empty archive block")
	}
	b.Device, b.Key = points[0].DeviceID, points[0].Key
	b.First, b.Last = points[0].ObservedMS, points[0].ObservedMS
	for _, p := range points {
		if p.DeviceID != b.Device || p.Key != b.Key {
			return b, errors.New("archive block mixes series")
		}
		if p.ObservedMS < b.First {
			b.First = p.ObservedMS
		}
		if p.ObservedMS > b.Last {
			b.Last = p.ObservedMS
		}
		if p.DefinitionID == "" {
			if b.RawFirst == 0 || p.ObservedMS < b.RawFirst {
				b.RawFirst = p.ObservedMS
			}
			if p.ObservedMS > b.RawLast {
				b.RawLast = p.ObservedMS
			}
		}
	}
	plain, err := packArchive(points)
	if err != nil {
		return b, err
	}
	if len(plain) > archivePlainLimit {
		return b, errors.New("archive block exceeds plain byte budget")
	}
	h := sha256.Sum256(plain)
	b.Hash = hex.EncodeToString(h[:])
	b.ID = b.Hash
	b.Count, b.PlainBytes = int64(len(points)), int64(len(plain))
	var compressed bytes.Buffer
	w, err := gzip.NewWriterLevel(&compressed, gzip.DefaultCompression)
	if err != nil {
		return b, err
	}
	if _, err = w.Write(plain); err != nil {
		return b, err
	}
	if err = w.Close(); err != nil {
		return b, err
	}
	b.Payload = compressed.Bytes()
	return b, nil
}

func decodeArchive(b archiveBlock) ([]model.Observation, error) {
	if b.PlainBytes < 1 || b.PlainBytes > archivePlainLimit || b.Count < 1 || b.Count > 4096 {
		return nil, errors.New("archive metadata exceeds limits")
	}
	r, err := gzip.NewReader(bytes.NewReader(b.Payload))
	if err != nil {
		return nil, fmt.Errorf("archive %s: %w", b.ID, err)
	}
	plain, err := io.ReadAll(io.LimitReader(r, b.PlainBytes+1))
	closeErr := r.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	h := sha256.Sum256(plain)
	if int64(len(plain)) != b.PlainBytes || hex.EncodeToString(h[:]) != b.Hash {
		return nil, errors.New("archive checksum or length mismatch")
	}
	var points []model.Observation
	if bytes.HasPrefix(plain, []byte("smartfactory-observations-jsonl-v1\n")) {
		points, err = unpackLegacyArchive(plain, b.Count)
	} else {
		points, err = unpackArchive(plain, b.Count)
	}
	if err != nil {
		return nil, err
	}
	for _, p := range points {
		if p.DeviceID != b.Device || p.Key != b.Key || p.ObservedMS < b.First || p.ObservedMS > b.Last {
			return nil, errors.New("archive series or range mismatch")
		}
	}

	return points, nil
}

func unpackLegacyArchive(plain []byte, count int64) ([]model.Observation, error) {
	points := make([]model.Observation, 0, count)
	scanner := bufio.NewScanner(bytes.NewReader(plain))
	scanner.Buffer(make([]byte, 4096), archivePlainLimit)
	scanner.Scan()
	for scanner.Scan() {
		var p model.Observation
		if err := DecodeJSON(scanner.Bytes(), &p); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if int64(len(points)) != count {
		return nil, errors.New("archive count mismatch")
	}
	return points, nil
}

const archiveColumns = "id,device_id,key,first_ms,last_ms,raw_first_ms,raw_last_ms,point_count,plain_bytes,sha256,payload"

func scanArchive(row interface{ Scan(...any) error }) (archiveBlock, error) {
	var b archiveBlock
	err := row.Scan(&b.ID, &b.Device, &b.Key, &b.First, &b.Last, &b.RawFirst, &b.RawLast, &b.Count, &b.PlainBytes, &b.Hash, &b.Payload)
	return b, err
}
func (t *Tx) saveArchive(b archiveBlock) error {
	_, err := t.ExecContext(t.Ctx, "INSERT INTO observation_archives("+archiveColumns+") VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)", b.ID, b.Device, b.Key, b.First, b.Last, b.RawFirst, b.RawLast, b.Count, b.PlainBytes, b.Hash, b.Payload)
	return err
}

// ArchiveObservations commits the verified compressed block and retirement of
// its exact hot-table prefix atomically. A failed insert leaves every hot row.
func (s *Store) ArchiveObservations(ctx context.Context) (ArchiveStats, error) {
	stats := ArchiveStats{}
	p := s.Policy().Archive
	if !p.Enabled {
		return stats, nil
	}
	if p.HotHours < 1 || p.BlockPoints < 128 || p.BlockPoints > 4096 || p.BlocksPerRun < 1 || p.BlocksPerRun > 64 {
		return stats, errors.New("invalid archive policy")
	}
	cut := s.Now().Add(-time.Duration(p.HotHours) * time.Hour).UnixMilli()
	for n := 0; n < p.BlocksPerRun; n++ {
		var committed archiveBlock
		err := s.Write(ctx, func(tx *Tx) error {
			var device, key string
			err := tx.QueryRowContext(ctx, "SELECT device_id,key FROM observations WHERE observed_ms<$1 ORDER BY observed_ms LIMIT 1", cut).Scan(&device, &key)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT data FROM observations WHERE device_id=$1 AND key=$2 AND observed_ms<$3 ORDER BY observed_ms,id LIMIT %d", p.BlockPoints), device, key, cut)
			if err != nil {
				return err
			}
			points := []model.Observation{}
			plainBytes := 0
			for rows.Next() {
				var raw string
				if err = rows.Scan(&raw); err != nil {
					rows.Close()
					return err
				}
				if plainBytes+len(raw)+1 > archivePlainLimit {
					break
				}
				var point model.Observation
				if err = DecodeJSON([]byte(raw), &point); err != nil {
					rows.Close()
					return err
				}
				points = append(points, point)
				plainBytes += len(raw) + 1
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if len(points) == 0 {
				return errors.New("single observation exceeds archive block budget")
			}
			block, err := encodeArchive(points)
			if err != nil {
				return err
			}
			if _, err = decodeArchive(block); err != nil {
				return err
			}
			if err = tx.saveArchive(block); err != nil {
				return err
			}
			last := points[len(points)-1]
			result, err := tx.ExecContext(ctx, "DELETE FROM observations WHERE device_id=$1 AND key=$2 AND (observed_ms<$3 OR (observed_ms=$3 AND id<=$4))", device, key, last.ObservedMS, last.ID)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil || count != int64(len(points)) {
				return errors.New("archive retirement count mismatch")
			}
			committed = block
			return nil
		})
		if err != nil {
			return stats, err
		}
		if committed.Count == 0 {
			break
		}
		stats.Blocks++
		stats.Points += committed.Count
		stats.PlainBytes += committed.PlainBytes
		stats.CompressedBytes += int64(len(committed.Payload))
	}
	if stats.Points > 0 && s.Driver == "pgx" {
		if err := s.retireEmptyArchivedDays(ctx, cut); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// Fully archived daily partitions can release their heap and indexes. Late
// observations recreate the affected partition through EnsureDay.
func (s *Store) retireEmptyArchivedDays(ctx context.Context, cut int64) error {
	return s.Write(ctx, func(tx *Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT tablename FROM pg_tables WHERE schemaname=current_schema() AND tablename LIKE 'observations_%'")
		if err != nil {
			return err
		}
		names := []string{}
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			day, e := time.Parse("20060102", strings.TrimPrefix(name, "observations_"))
			if e == nil && day.AddDate(0, 0, 1).UnixMilli() <= cut {
				names = append(names, name)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, name := range names {
			var exists bool
			if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+name+" LIMIT 1)").Scan(&exists); err != nil {
				return err
			}
			if !exists {
				if _, err = tx.ExecContext(ctx, "DROP TABLE "+name); err != nil {
					return err
				}
				delete(s.partitions, name)
			}
		}
		return nil
	})
}

func observationLater(a, b model.Observation) bool {
	if a.ObservedMS != b.ObservedMS {
		return a.ObservedMS > b.ObservedMS
	}
	if a.DeviceID != b.DeviceID {
		return a.DeviceID < b.DeviceID
	}
	if a.Key != b.Key {
		return a.Key < b.Key
	}
	return a.ID < b.ID
}
func mergeObservations(points []model.Observation, limit int) []model.Observation {
	values := map[string]model.Observation{}
	for _, p := range points {
		key := fmt.Sprintf("raw:%s:%d", p.ID, p.ObservedMS)
		if p.DefinitionID != "" {
			key = fmt.Sprintf("derived:%s:%s:%d", p.DeviceID, p.Key, p.ObservedMS)
		}
		old, exists := values[key]
		if !exists || p.Revision > old.Revision || (p.Revision == old.Revision && (p.ReceivedMS > old.ReceivedMS || (p.ReceivedMS == old.ReceivedMS && p.ID > old.ID))) {
			values[key] = p
		}
	}
	result := make([]model.Observation, 0, len(values))
	for _, p := range values {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool { return observationLater(result[i], result[j]) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}
func (s *Store) archiveQuery(ctx context.Context, opts Query, hot []model.Observation) ([]model.Observation, error) {
	var q strings.Builder
	args := []any{opts.FromMS, opts.ToMS}
	q.WriteString("SELECT " + archiveColumns + " FROM observation_archives WHERE last_ms >= $1 AND first_ms <= $2")
	if opts.RawOnly {
		q.WriteString(" AND raw_last_ms>0")
	}
	appendIn(&q, &args, "device_id", opts.DeviceIDs)
	appendIn(&q, &args, "key", opts.Keys)
	q.WriteString(" ORDER BY last_ms DESC,id")
	rows, err := s.DB.QueryContext(ctx, q.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := mergeObservations(hot, opts.Limit+1)
	for rows.Next() {
		b, err := scanArchive(rows)
		if err != nil {
			return nil, err
		}
		if len(points) >= opts.Limit+1 && b.Last < points[len(points)-1].ObservedMS {
			break
		}
		archived, err := decodeArchive(b)
		if err != nil {
			return nil, err
		}
		for _, p := range archived {
			if p.ObservedMS >= opts.FromMS && p.ObservedMS <= opts.ToMS && (!opts.RawOnly || p.DefinitionID == "") {
				points = append(points, p)
			}
		}
		points = mergeObservations(points, opts.Limit+1)
	}
	return points, rows.Err()
}

func (s *Store) ObservationRange(ctx context.Context, devices []string, rawOnly bool) (int64, int64, error) {
	var q strings.Builder
	args := []any{}
	q.WriteString("SELECT min(observed_ms),max(observed_ms) FROM observations WHERE 1=1")
	if rawOnly {
		q.WriteString(" AND definition_id=''")
	}
	appendIn(&q, &args, "device_id", devices)
	var first, last sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, q.String(), args...).Scan(&first, &last); err != nil {
		return 0, 0, err
	}
	q.Reset()
	args = nil
	cols := "min(first_ms),max(last_ms)"
	if rawOnly {
		cols = "min(raw_first_ms),max(raw_last_ms)"
	}
	q.WriteString("SELECT " + cols + " FROM observation_archives WHERE 1=1")
	if rawOnly {
		q.WriteString(" AND raw_last_ms>0")
	}
	appendIn(&q, &args, "device_id", devices)
	var a, b sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, q.String(), args...).Scan(&a, &b); err != nil {
		return 0, 0, err
	}
	if a.Valid && (!first.Valid || a.Int64 < first.Int64) {
		first = a
	}
	if b.Valid && (!last.Valid || b.Int64 > last.Int64) {
		last = b
	}
	return first.Int64, last.Int64, nil
}

// FindObservation retains the original observation identity for a delayed
// ThingsBoard callback, including an older derived revision in an archive.
func (s *Store) FindObservation(ctx context.Context, id string, at int64, device, key string) (model.Observation, error) {
	var point model.Observation
	var raw string
	err := s.DB.QueryRowContext(ctx, "SELECT data FROM observations WHERE id=$1 AND observed_ms=$2 AND device_id=$3 AND key=$4", id, at, device, key).Scan(&raw)
	if err == nil {
		err = DecodeJSON([]byte(raw), &point)
		return point, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return point, err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+archiveColumns+" FROM observation_archives WHERE device_id=$1 AND key=$2 AND first_ms<=$3 AND last_ms>=$3", device, key, at)
	if err != nil {
		return point, err
	}
	defer rows.Close()
	for rows.Next() {
		block, err := scanArchive(rows)
		if err != nil {
			return point, err
		}
		points, err := decodeArchive(block)
		if err != nil {
			return point, err
		}
		for _, p := range points {
			if p.ID == id && p.ObservedMS == at {
				return p, nil
			}
		}
	}
	if err = rows.Err(); err != nil {
		return point, err
	}
	return point, ErrNotFound
}

func (s *Store) observationSeries(ctx context.Context, from, to int64) ([][2]string, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT DISTINCT device_id,key FROM observations WHERE observed_ms>=$1 AND observed_ms<$2 UNION SELECT DISTINCT device_id,key FROM observation_archives WHERE last_ms>=$1 AND first_ms<$2", from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	series := [][2]string{}
	for rows.Next() {
		var item [2]string
		if err = rows.Scan(&item[0], &item[1]); err != nil {
			return nil, err
		}
		series = append(series, item)
	}
	return series, rows.Err()
}

// walkSeries splits dense time spans while bounding the decoded point set.
func (s *Store) walkSeries(ctx context.Context, pair [2]string, from, to int64, visit func(model.Observation) error) error {
	result, err := s.Query(ctx, Query{DeviceIDs: []string{pair[0]}, Keys: []string{pair[1]}, FromMS: from, ToMS: to - 1, Limit: 40000})
	if err != nil {
		return err
	}
	if result.Truncated {
		if to-from <= 1 {
			return errors.New("more than 40000 observations in one series millisecond")
		}
		middle := from + (to-from)/2
		if err = s.walkSeries(ctx, pair, from, middle, visit); err != nil {
			return err
		}
		return s.walkSeries(ctx, pair, middle, to, visit)
	}
	for _, p := range result.Points {
		if err = visit(p); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) expireArchives(cut int64) (int64, error) {
	var removed int64
	for {
		b, err := scanArchive(t.QueryRowContext(t.Ctx, "SELECT "+archiveColumns+" FROM observation_archives WHERE first_ms<$1 AND last_ms>=$1 ORDER BY first_ms,id LIMIT 1", cut))
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return 0, err
		}
		points, err := decodeArchive(b)
		if err != nil {
			return 0, err
		}
		keep := points[:0]
		for _, p := range points {
			if p.ObservedMS >= cut {
				keep = append(keep, p)
			} else {
				removed++
			}
		}
		next, err := encodeArchive(keep)
		if err != nil {
			return 0, err
		}
		if err = t.saveArchive(next); err != nil {
			return 0, err
		}
		if _, err = t.ExecContext(t.Ctx, "DELETE FROM observation_archives WHERE id=$1", b.ID); err != nil {
			return 0, err
		}
	}

	var whole int64
	if err := t.QueryRowContext(t.Ctx, "SELECT COALESCE(sum(point_count),0) FROM observation_archives WHERE last_ms<$1", cut).Scan(&whole); err != nil {
		return 0, err
	}
	_, err := t.ExecContext(t.Ctx, "DELETE FROM observation_archives WHERE last_ms<$1", cut)
	return removed + whole, err
}

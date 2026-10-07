package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"competition2026/product/platform/pkg/model"
)

var (
	ErrQueryExpired       = errors.New("cursor_expired")
	ErrQueryReset         = errors.New("projection_rebuilt")
	ErrQueryAuthorization = errors.New("authorization_changed")
	ErrQueryBudget        = errors.New("budget_exceeded")
	ErrQueryChanged       = errors.New("query_changed")
	ErrQueryInvalid       = errors.New("invalid_cursor")
)

type QueryScope struct {
	Roots        []string
	All          bool
	AuthRevision int64
}
type QueryPosition struct {
	Epoch    string
	Sequence int64
	AfterID  string
	AfterMS  int64
}
type QueryReadResult struct {
	State    QueryState
	Position QueryPosition
	Rows     []model.QueryRow
	HasMore  bool
	Metadata model.QueryMetadata
}

// QueryPage reads the watermark, historical row interval and metadata in one
// database snapshot. Candidate authorization is part of the SQL predicate.
func (s *Store) QueryPage(ctx context.Context, kind string, opts model.QueryRequest, scope QueryScope, position QueryPosition) (QueryReadResult, error) {
	out := QueryReadResult{Rows: []model.QueryRow{}, Metadata: emptyQueryMetadata()}
	isolation := sql.LevelSerializable
	if s.Driver == "pgx" {
		isolation = sql.LevelRepeatableRead
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: isolation, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	out.State, err = queryState(ctx, tx)
	if err != nil {
		return out, err
	}
	if position.Epoch == "" {
		position = QueryPosition{Epoch: out.State.Epoch, Sequence: out.State.Head}
	}
	if position.Epoch != out.State.Epoch {
		return out, ErrQueryReset
	}
	if position.Sequence < out.State.Floor || position.Sequence > out.State.Head {
		return out, ErrQueryExpired
	}
	if scope.AuthRevision >= 0 && scope.AuthRevision != out.State.AuthRevision {
		return out, ErrQueryAuthorization
	}
	out.Position = position
	query, args := s.queryRowsSQL(kind, opts, scope, position, opts.Limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var row model.QueryRow
		var seq int64
		var raw []byte
		if err = rows.Scan(&row.Kind, &row.ID, &seq, &row.Version, &row.SortMS, &raw); err != nil {
			rows.Close()
			return out, err
		}
		row.Revision = decimalQueryRevision(seq)
		row.Data, err = unpackQueryData(raw)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Rows = append(out.Rows, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Rows) > opts.Limit {
		out.HasMore = true
		out.Rows = out.Rows[:opts.Limit]
	}
	if kind == "trend" {
		if err = s.queryMetadata(ctx, tx, opts, scope, position, &out); err != nil {
			return out, err
		}
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

func emptyQueryMetadata() model.QueryMetadata {
	return model.QueryMetadata{Quality: model.QualitySummary{Completeness: "unknown"}, QualityScope: "page", Sources: []model.SourceState{}, Revisions: []model.Revision{}, Gaps: []model.DataGap{}}
}

func (s *Store) queryRowsSQL(kind string, opts model.QueryRequest, scope QueryScope, pos QueryPosition, limit int) (string, []any) {
	args := []any{}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	var q strings.Builder
	if !scope.All && len(scope.Roots) > 0 {
		q.WriteString("WITH RECURSIVE permitted(id,depth) AS (")
		for i, root := range scope.Roots {
			if i > 0 {
				q.WriteString(" UNION ")
			}
			collation := "BINARY"
			if s.Driver == "pgx" {
				collation = `"C"`
			}
			q.WriteString("SELECT CAST(" + arg(root) + " AS TEXT) COLLATE " + collation + ",0")
		}
		q.WriteString(" UNION SELECT e.id,p.depth+1 FROM sf_query_rows e JOIN permitted p ON e.parent_id=p.id WHERE e.kind='entities' AND e.valid_to=0 AND e.deleted=0 AND p.depth<64) ")
	}
	q.WriteString("SELECT r.kind,r.id,r.valid_from,r.version,r.sort_ms,r.data FROM sf_query_rows r WHERE r.kind=" + arg(kind) + " AND r.deleted=0 AND r.valid_from<=" + arg(pos.Sequence))
	q.WriteString(" AND (r.valid_to=0 OR r.valid_to>" + arg(pos.Sequence) + ")")
	if !scope.All {
		if len(scope.Roots) == 0 {
			q.WriteString(" AND 1=0")
		} else {
			q.WriteString(" AND EXISTS(SELECT 1 FROM sf_query_resources x WHERE x.kind=r.kind AND x.id=r.id AND x.valid_from=r.valid_from) AND NOT EXISTS(SELECT 1 FROM sf_query_resources x WHERE x.kind=r.kind AND x.id=r.id AND x.valid_from=r.valid_from AND NOT EXISTS(SELECT 1 FROM permitted p WHERE p.id=x.resource_id))")
		}
	}
	if len(opts.ResourceIDs) > 0 {
		q.WriteString(" AND EXISTS(SELECT 1 FROM sf_query_resources selected WHERE selected.kind=r.kind AND selected.id=r.id AND selected.valid_from=r.valid_from AND selected.resource_id IN (")
		for i, id := range opts.ResourceIDs {
			if i > 0 {
				q.WriteByte(',')
			}
			q.WriteString(arg(id))
		}
		q.WriteString("))")
	}
	if len(opts.Keys) > 0 {
		q.WriteString(" AND r.metric_key IN (")
		for i, key := range opts.Keys {
			if i > 0 {
				q.WriteByte(',')
			}
			q.WriteString(arg(key))
		}
		q.WriteByte(')')
	}
	for _, filter := range []struct{ column, value string }{{"definition_id", opts.DefinitionID}, {"status", opts.Status}, {"entity_kind", opts.EntityKind}, {"assignee_id", opts.AssigneeID}, {"handling_status", opts.HandlingStatus}} {
		if filter.value != "" {
			q.WriteString(" AND r." + filter.column + "=" + arg(filter.value))
		}
	}
	if opts.Active != nil {
		v := 0
		if *opts.Active {
			v = 1
		}
		q.WriteString(" AND r.active=" + arg(v))
	}
	if opts.Acknowledged != nil {
		v := 0
		if *opts.Acknowledged {
			v = 1
		}
		q.WriteString(" AND r.acknowledged=" + arg(v))
	}
	if opts.Search != "" {
		needle := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(strings.ToLower(opts.Search))
		q.WriteString(" AND (r.name LIKE " + arg("%"+needle+"%") + " ESCAPE '\\' OR LOWER(r.id) LIKE " + arg("%"+needle+"%") + " ESCAPE '\\')")
	}
	if kind == "trend" {
		q.WriteString(" AND r.resolution=" + arg(opts.Resolution))
	}
	if opts.FromMS > 0 {
		if kind == "gaps" {
			q.WriteString(" AND r.version>=" + arg(opts.FromMS))
		} else {
			q.WriteString(" AND r.sort_ms>=" + arg(opts.FromMS))
		}
	}
	if opts.ToMS > 0 {
		q.WriteString(" AND r.sort_ms<=" + arg(opts.ToMS))
	}
	if pos.AfterID != "" {
		q.WriteString(" AND (r.sort_ms<" + arg(pos.AfterMS) + " OR (r.sort_ms=" + arg(pos.AfterMS) + " AND r.id>" + arg(pos.AfterID) + "))")
	}
	q.WriteString(" ORDER BY r.sort_ms DESC,r.id ASC LIMIT " + arg(limit))
	return q.String(), args
}

func (s *Store) queryMetadata(ctx context.Context, tx *sql.Tx, opts model.QueryRequest, scope QueryScope, pos QueryPosition, out *QueryReadResult) error {
	for _, row := range out.Rows {
		var point model.Observation
		if err := DecodeJSON(row.Data, &point); err != nil {
			return err
		}
		if opts.Resolution != "raw" {
			var aggregate Aggregate
			raw, err := jsonQuery(point.Value)
			if err != nil {
				return err
			}
			if err = DecodeJSON(raw, &aggregate); err != nil {
				return err
			}
			out.Metadata.Quality.Good += aggregate.Count
			out.Metadata.Quality.Excluded += aggregate.Excluded
			continue
		}
		switch point.Quality {
		case "GOOD":
			out.Metadata.Quality.Good++
		case "BAD":
			out.Metadata.Quality.Bad++
			out.Metadata.Quality.Excluded++
		default:
			out.Metadata.Quality.Uncertain++
			out.Metadata.Quality.Excluded++
		}
	}
	for _, kind := range []string{"sources", "revisions", "gaps"} {
		aux := model.QueryRequest{ResourceIDs: opts.ResourceIDs, Keys: opts.Keys, FromMS: opts.FromMS, ToMS: opts.ToMS}
		if kind == "sources" {
			aux = model.QueryRequest{}
		}
		p := pos
		p.AfterID = ""
		query, args := s.queryRowsSQL(kind, aux, scope, p, 2001)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			count++
			if count > 2000 {
				rows.Close()
				return ErrQueryBudget
			}
			var k, id string
			var seq, version, sortMS int64
			var packed []byte
			if err = rows.Scan(&k, &id, &seq, &version, &sortMS, &packed); err != nil {
				rows.Close()
				return err
			}
			raw, e := unpackQueryData(packed)
			if e != nil {
				rows.Close()
				return e
			}
			switch kind {
			case "sources":
				var v model.SourceState
				if e = DecodeJSON(raw, &v); e == nil {
					if s.Now().UnixMilli()-v.LastSeenMS > s.Policy().OfflineMS {
						v.Status = "offline"
						v.Reason = fmt.Sprintf("heartbeat older than %d ms", s.Policy().OfflineMS)
					}
					out.Metadata.Sources = append(out.Metadata.Sources, v)
				}
			case "revisions":
				var v model.Revision
				if e = DecodeJSON(raw, &v); e == nil {
					out.Metadata.Revisions = append(out.Metadata.Revisions, v)
				}
			case "gaps":
				var v model.DataGap
				if e = DecodeJSON(raw, &v); e == nil {
					out.Metadata.Gaps = append(out.Metadata.Gaps, v)
				}
			}
			if e != nil {
				rows.Close()
				return e
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if len(out.Metadata.Gaps) > 0 {
		out.Metadata.Quality.Completeness = "incomplete"
		var missing int64
		known := true
		for _, gap := range out.Metadata.Gaps {
			if gap.FromMS >= opts.FromMS && gap.ToMS <= opts.ToMS {
				missing += gap.Missing
			} else {
				known = false
			}
		}
		if known {
			out.Metadata.Quality.Missing = &missing
		}
	}
	return nil
}

func jsonQuery(value any) ([]byte, error) { return json.Marshal(value) }

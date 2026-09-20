package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type Change struct {
	Sequence int64    `json:"sequence"`
	Document Document `json:"document"`
}

func (t *Tx) RecordChange(d Document) error {
	switch d.Kind {
	case "entity", "definition", "department", "asset_proposal", "dashboard":
	default:
		return nil
	}
	_, e := t.ExecContext(t.Ctx, "INSERT INTO sync_changes(sequence,kind,id,version,updated_ms,data) SELECT COALESCE(MAX(sequence),0)+1,$1,$2,$3,$4,$5 FROM sync_changes", d.Kind, d.ID, d.Version, d.UpdatedMS, string(d.Data))
	return e
}
func (s *Store) Changes(ctx context.Context, after int64, limit int) ([]Change, error) {
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	rows, e := s.DB.QueryContext(ctx, "SELECT sequence,kind,id,version,updated_ms,data FROM sync_changes WHERE sequence>$1 ORDER BY sequence LIMIT $2", after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var c Change
		var raw string
		if e = rows.Scan(&c.Sequence, &c.Document.Kind, &c.Document.ID, &c.Document.Version, &c.Document.UpdatedMS, &raw); e != nil {
			return nil, e
		}
		c.Document.Data = json.RawMessage(raw)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ImportDocument retains the source version and effective timestamp. Only an
// authenticated owner may select the documents supplied to this method.
func (t *Tx) ImportDocument(d Document) (bool, error) {
	if d.Version < 1 || d.ID == "" || !json.Valid(d.Data) {
		return false, errors.New("invalid synchronized document")
	}
	previous, e := scanDocument(t.QueryRowContext(t.Ctx, "SELECT kind,id,version,updated_ms,data FROM document_versions WHERE kind=$1 AND id=$2 AND version=$3", d.Kind, d.ID, d.Version))
	if e == nil {
		var a, b any
		if DecodeJSON(previous.Data, &a) != nil || DecodeJSON(d.Data, &b) != nil || Hash(a) != Hash(b) {
			return false, fmt.Errorf("%w: %s/%s version %d", ErrConflict, d.Kind, d.ID, d.Version)
		}
		return false, nil
	}
	if !errors.Is(e, ErrNotFound) {
		return false, e
	}
	if _, e = t.ExecContext(t.Ctx, "INSERT INTO document_versions(kind,id,version,updated_ms,data) VALUES($1,$2,$3,$4,$5)", d.Kind, d.ID, d.Version, d.UpdatedMS, string(d.Data)); e != nil {
		return false, e
	}
	result, e := t.ExecContext(t.Ctx, "INSERT INTO documents(kind,id,version,updated_ms,data) VALUES($1,$2,$3,$4,$5) ON CONFLICT(kind,id) DO UPDATE SET version=excluded.version,updated_ms=excluded.updated_ms,data=excluded.data WHERE excluded.version>documents.version", d.Kind, d.ID, d.Version, d.UpdatedMS, string(d.Data))
	if e != nil {
		return false, e
	}
	count, e := result.RowsAffected()
	return count > 0, e
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"competition2026/product/platform/pkg/model"
)

func (s *Store) Investigation(ctx context.Context, id string) (model.Investigation, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, "SELECT data FROM sf_ai_investigations WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Investigation{}, ErrNotFound
	}
	if err != nil {
		return model.Investigation{}, err
	}
	var out model.Investigation
	err = DecodeJSON([]byte(raw), &out)
	return out, err
}

func (s *Store) InvestigationIDs(ctx context.Context, user, after string, limit int) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM sf_ai_investigations WHERE user_id=$1 AND id>$2 ORDER BY id LIMIT $3", user, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (tx *Tx) PutInvestigation(value model.Investigation, expected int64) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = tx.ExecContext(tx.Ctx, "INSERT INTO sf_ai_investigations(id,user_id,version,created_ms,updated_ms,data) VALUES($1,$2,$3,$4,$5,$6)", value.ID, value.UserID, value.Version, value.CreatedMS, value.UpdatedMS, string(raw))
		return err
	}
	result, err := tx.ExecContext(tx.Ctx, "UPDATE sf_ai_investigations SET version=$1,updated_ms=$2,data=$3 WHERE id=$4 AND version=$5", value.Version, value.UpdatedMS, string(raw), value.ID, expected)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) InvestigationEvidence(ctx context.Context, id string) (model.InvestigationEvidence, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, "SELECT data FROM sf_ai_evidence WHERE id=$1", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.InvestigationEvidence{}, ErrNotFound
	}
	if err != nil {
		return model.InvestigationEvidence{}, err
	}
	var out model.InvestigationEvidence
	err = DecodeJSON([]byte(raw), &out)
	return out, err
}

func (s *Store) InvestigationEvidenceList(ctx context.Context, id string) ([]model.InvestigationEvidence, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT data FROM sf_ai_evidence WHERE investigation_id=$1 ORDER BY ordinal", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.InvestigationEvidence{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e model.InvestigationEvidence
		if err = DecodeJSON([]byte(raw), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (tx *Tx) PutInvestigationEvidence(value model.InvestigationEvidence, create bool) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if create {
		_, err = tx.ExecContext(tx.Ctx, "INSERT INTO sf_ai_evidence(id,investigation_id,ordinal,tool_call_id,data) VALUES($1,$2,$3,$4,$5)", value.ID, value.InvestigationID, value.Ordinal, value.ToolCallID, string(raw))
		return err
	}
	result, err := tx.ExecContext(tx.Ctx, "UPDATE sf_ai_evidence SET data=$1 WHERE id=$2 AND investigation_id=$3", string(raw), value.ID, value.InvestigationID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return ErrNotFound
	}
	return err
}

// AIDocuments bounds the database candidate scan. Authorization and user
// visible continuation are applied by the investigation application.
func (s *Store) AIDocuments(ctx context.Context, kind, after string, limit int) ([]Document, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT kind,id,version,updated_ms,data FROM documents WHERE kind=$1 AND id>$2 ORDER BY id LIMIT $3", kind, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		doc, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}

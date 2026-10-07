package store

import (
	"context"
	"database/sql"
)

func (s *Store) migrateInvestigations(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sf_ai_investigations(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,version BIGINT NOT NULL,created_ms BIGINT NOT NULL,updated_ms BIGINT NOT NULL,data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS sf_ai_investigations_owner ON sf_ai_investigations(user_id,id);
CREATE TABLE IF NOT EXISTS sf_ai_evidence(id TEXT PRIMARY KEY,investigation_id TEXT NOT NULL,ordinal BIGINT NOT NULL,tool_call_id TEXT NOT NULL,data TEXT NOT NULL,UNIQUE(investigation_id,tool_call_id),UNIQUE(investigation_id,ordinal));
CREATE INDEX IF NOT EXISTS sf_ai_evidence_investigation ON sf_ai_evidence(investigation_id,ordinal);`)
	return err
}

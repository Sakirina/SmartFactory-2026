package store

import (
	"context"
	"database/sql"
)

func (s *Store) migrateControl(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE execution_transitions (
 execution_id TEXT NOT NULL, version BIGINT NOT NULL, at_ms BIGINT NOT NULL,
 data TEXT NOT NULL, PRIMARY KEY(execution_id,version)
 );
 CREATE TABLE control_evidence (
 id TEXT PRIMARY KEY, execution_id TEXT NOT NULL, command_id TEXT NOT NULL,
 collected_ms BIGINT NOT NULL, data TEXT NOT NULL
 );
 CREATE INDEX control_evidence_execution ON control_evidence(execution_id,collected_ms,id);`)
	return err
}

package store

import (
	"context"
	"database/sql"
)

func (s *Store) migrateAnalysis(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sf_analysis_snapshots(id TEXT PRIMARY KEY,sha256 TEXT NOT NULL,data TEXT NOT NULL,created_ms BIGINT NOT NULL);
 CREATE TABLE IF NOT EXISTS sf_analysis_steps(run_id TEXT NOT NULL,input_index BIGINT NOT NULL,definition_id TEXT NOT NULL,input_identity TEXT NOT NULL,input_revision BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(run_id,input_index));
 CREATE TABLE IF NOT EXISTS sf_shadow_inputs(candidate_id TEXT NOT NULL,epoch BIGINT NOT NULL,input_identity TEXT NOT NULL,input_revision BIGINT NOT NULL,generation BIGINT NOT NULL,payload_hash TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(candidate_id,epoch,input_identity,input_revision));
 CREATE TABLE IF NOT EXISTS sf_analysis_formal(definition_id TEXT NOT NULL,input_identity TEXT NOT NULL,input_revision BIGINT NOT NULL,rule_version BIGINT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(definition_id,input_identity,input_revision,rule_version));
 CREATE INDEX IF NOT EXISTS sf_analysis_steps_input ON sf_analysis_steps(definition_id,input_identity,input_revision);
 CREATE INDEX IF NOT EXISTS sf_shadow_inputs_cursor ON sf_shadow_inputs(candidate_id,epoch,generation);`)
	return err
}

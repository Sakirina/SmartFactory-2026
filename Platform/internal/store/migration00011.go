package store

import (
	"context"
	"database/sql"
)

func (s *Store) migrateReleases(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sf_release_reports(identity_id TEXT NOT NULL,deployment_id TEXT NOT NULL,generation BIGINT NOT NULL,instance_epoch BIGINT NOT NULL,sequence BIGINT NOT NULL,payload_hash TEXT NOT NULL,data TEXT NOT NULL,received_ms BIGINT NOT NULL,PRIMARY KEY(identity_id,deployment_id,generation,instance_epoch,sequence));
CREATE INDEX IF NOT EXISTS sf_release_reports_deployment ON sf_release_reports(deployment_id,received_ms);
CREATE TABLE IF NOT EXISTS sf_release_artifacts(sha256 TEXT PRIMARY KEY,size_bytes BIGINT NOT NULL,build_json TEXT NOT NULL,created_ms BIGINT NOT NULL);`)
	return err
}

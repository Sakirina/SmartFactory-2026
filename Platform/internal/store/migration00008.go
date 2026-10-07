package store

import (
	"context"
	"database/sql"
)

func (s *Store) migrateBusiness(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sf_business_requests(scope TEXT NOT NULL,request_id TEXT NOT NULL,payload_hash TEXT NOT NULL,response TEXT NOT NULL,created_ms BIGINT NOT NULL,PRIMARY KEY(scope,request_id));
CREATE TABLE IF NOT EXISTS sf_business_source_sequence(source_id TEXT PRIMARY KEY,sequence BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS sf_alarm_operation_sources(source_id TEXT NOT NULL,sequence BIGINT NOT NULL,operation_id TEXT NOT NULL,payload_hash TEXT NOT NULL,PRIMARY KEY(source_id,sequence));
CREATE INDEX IF NOT EXISTS sf_business_requests_time ON sf_business_requests(created_ms);`)
	return err
}

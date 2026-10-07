package store

import (
	"context"
	"database/sql"
)

// migrateInboxExpiry adopts installations created before inbox expiry existed.
func (s *Store) migrateInboxExpiry(ctx context.Context, tx *sql.Tx) error {
	query := "SELECT count(*) FROM pragma_table_info('inbox') WHERE name='expires_ms'"
	if s.Driver == "pgx" {
		query = "SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='inbox' AND column_name='expires_ms'"
	}
	var exists int
	if err := tx.QueryRowContext(ctx, query).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE inbox ADD COLUMN expires_ms BIGINT NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS inbox_expiry ON inbox(expires_ms) WHERE expires_ms>0")
	return err
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"

	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivermigrate"
)

// River owns its numbered migrations. They run individually outside a Goose
// transaction, then Goose records this bridge and its checksum atomically with
// our business identity table. An interrupted River migration resumes before
// Goose version 4 is recorded. Versions 1 through 3 retain their original bytes.
func (s *Store) migrateRiver(ctx context.Context) error {
	migrator, err := rivermigrate.New(s.RiverDriver(), &rivermigrate.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return err
	}
	if _, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, &rivermigrate.MigrateOpts{TargetVersion: 8}); err != nil {
		return fmt.Errorf("River v0.48.0 migration: %w", err)
	}
	result, err := migrator.Validate(ctx, &rivermigrate.ValidateOpts{TargetVersion: 8})
	if err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("River schema validation: %v", result.Messages)
	}
	return nil
}

func (s *Store) RiverDriver() riverdriver.Driver[*sql.Tx] {
	if s.Driver == "pgx" {
		return riverdatabasesql.New(s.DB)
	}
	return riversqlite.New(s.DB)
}

func (s *Store) migrateTasks(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sf_tasks (
 id TEXT PRIMARY KEY, river_id BIGINT NOT NULL UNIQUE, kind TEXT NOT NULL,
 business_id TEXT NOT NULL, business_version BIGINT NOT NULL, outbox_id TEXT NOT NULL,
 resources TEXT NOT NULL, created_ms BIGINT NOT NULL, cancelled_ms BIGINT NOT NULL DEFAULT 0
 ); CREATE INDEX IF NOT EXISTS sf_tasks_business ON sf_tasks(kind,business_id,business_version);`)
	return err
}

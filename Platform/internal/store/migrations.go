package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

// The Go migration source is embedded as well as compiled, so any change to an
// already applied SQL or Go migration is detected before startup proceeds.
//
//go:embed migrations/*/*.sql migration00002.go migration00003.go migration00004.go migration00005.go migration00006.go migration00007.go migration00008.go migration00009.go migration00010.go migration00011.go
var migrationFiles embed.FS

var migrationMu sync.Mutex

type MigrationRecord struct {
	Version   int64  `json:"version"`
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	AppliedMS int64  `json:"applied_ms"`
}

func MigrationManifest(driver string) ([]MigrationRecord, error) {
	dialect := "sqlite"
	if driver == "pgx" {
		dialect = "postgres"
	}
	paths := []string{"migrations/" + dialect + "/00001_baseline.sql", "migration00002.go", "migration00003.go", "migration00004.go", "migration00005.go", "migration00006.go", "migration00007.go", "migration00008.go", "migration00009.go", "migration00010.go", "migration00011.go"}
	manifest := make([]MigrationRecord, 0, len(paths))
	for i, path := range paths {
		source, err := migrationFiles.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(source)
		manifest = append(manifest, MigrationRecord{Version: int64(i + 1), Name: path, SHA256: hex.EncodeToString(sum[:])})
	}
	return manifest, nil
}

func (s *Store) migrate(ctx context.Context) error {
	// SQLite has one connection per store; the process lock also prevents two
	// local startup paths racing while the migration metadata is first created.
	migrationMu.Lock()
	defer migrationMu.Unlock()
	provider, err := s.migrationProvider()
	if err != nil {
		return err
	}
	// A dedicated session lock spans the Goose/River handoff on PostgreSQL.
	// SQLite startup is serialized by migrationMu and retains one connection.
	if s.Driver == "pgx" {
		conn, lockErr := s.DB.Conn(ctx)
		if lockErr != nil {
			return lockErr
		}
		defer conn.Close()
		if _, lockErr = conn.ExecContext(ctx, "SELECT pg_advisory_lock(872190004)"); lockErr != nil {
			return lockErr
		}
		defer conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(872190004)")
	}
	if _, err = provider.UpTo(ctx, 3); err != nil {
		return fmt.Errorf("baseline schema migration: %w", err)
	}
	if err = s.migrateRiver(ctx); err != nil {
		return err
	}
	if _, err = provider.Up(ctx); err != nil {
		return fmt.Errorf("schema migration: %w", err)
	}
	return nil
}

func (s *Store) migrationProvider() (*goose.Provider, error) {
	dialect, directory := database.DialectSQLite3, "sqlite"
	if s.Driver == "pgx" {
		dialect, directory = database.DialectPostgres, "postgres"
	}
	base, err := database.NewStore(dialect, "goose_db_version")
	if err != nil {
		return nil, err
	}
	manifest, err := MigrationManifest(s.Driver)
	if err != nil {
		return nil, err
	}
	checks := &checksumStore{Store: base, sources: map[int64]MigrationRecord{}, now: s.Now}
	for _, record := range manifest {
		checks.sources[record.Version] = record
	}
	files, err := fs.Sub(migrationFiles, "migrations/"+directory)
	if err != nil {
		return nil, err
	}
	options := []goose.ProviderOption{
		goose.WithStore(checks),
		goose.WithDisableGlobalRegistry(true),
		goose.WithGoMigrations(
			goose.NewGoMigration(2, &goose.GoFunc{RunTx: s.migrateInboxExpiry}, &goose.GoFunc{RunTx: rejectMigrationDown}),
			goose.NewGoMigration(3, &goose.GoFunc{RunTx: s.migrateSyncHistory}, &goose.GoFunc{RunTx: rejectMigrationDown}),
			goose.NewGoMigration(4, &goose.GoFunc{RunTx: s.migrateTasks}, &goose.GoFunc{RunTx: rejectMigrationDown}),
			goose.NewGoMigration(5, &goose.GoFunc{RunTx: s.migrateControl}, &goose.GoFunc{RunTx: rejectMigrationDown}),
			goose.NewGoMigration(6, &goose.GoFunc{RunTx: s.migrateAnalysis}, &goose.GoFunc{RunTx: rejectMigrationDown}),
			goose.NewGoMigration(7, &goose.GoFunc{RunTx: s.migrateQueries}, &goose.GoFunc{RunTx: rejectMigrationDown}),
			goose.NewGoMigration(8, &goose.GoFunc{RunTx: s.migrateBusiness}, &goose.GoFunc{RunTx: rejectMigrationDown}),
			goose.NewGoMigration(9, &goose.GoFunc{RunTx: s.migrateInvestigations}, &goose.GoFunc{RunTx: rejectMigrationDown}),
			goose.NewGoMigration(10, &goose.GoFunc{RunTx: s.migrateNodeConfiguration}, &goose.GoFunc{RunTx: rejectMigrationDown}),
			goose.NewGoMigration(11, &goose.GoFunc{RunTx: s.migrateReleases}, &goose.GoFunc{RunTx: rejectMigrationDown}),
		),
	}
	if s.Driver == "pgx" {
		locker, err := lock.NewPostgresSessionLocker()
		if err != nil {
			return nil, err
		}
		options = append(options, goose.WithSessionLocker(locker))
	}
	return goose.NewProvider(goose.DialectCustom, s.DB, files, options...)
}

func rejectMigrationDown(context.Context, *sql.Tx) error {
	return errors.New("automatic schema downgrade is disabled; restore a verified backup or apply a forward migration")
}

type checksumStore struct {
	database.Store
	sources map[int64]MigrationRecord
	now     func() time.Time
}

func (s *checksumStore) CreateVersionTable(ctx context.Context, db database.DBTxConn) error {
	if err := s.Store.CreateVersionTable(ctx, db); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `CREATE TABLE sf_schema_migrations(version BIGINT PRIMARY KEY,name TEXT NOT NULL,sha256 TEXT NOT NULL,applied_ms BIGINT NOT NULL)`)
	return err
}

// Goose calls Insert inside the same transaction as the migration itself.
func (s *checksumStore) Insert(ctx context.Context, db database.DBTxConn, request database.InsertRequest) error {
	if err := s.Store.Insert(ctx, db, request); err != nil {
		return err
	}
	if request.Version == 0 {
		return nil
	}
	source, ok := s.sources[request.Version]
	if !ok {
		return fmt.Errorf("migration %d has no embedded checksum", request.Version)
	}
	_, err := db.ExecContext(ctx, "INSERT INTO sf_schema_migrations(version,name,sha256,applied_ms) VALUES($1,$2,$3,$4)", source.Version, source.Name, source.SHA256, s.now().UnixMilli())
	return err
}

func (s *checksumStore) Delete(context.Context, database.DBTxConn, int64) error {
	return errors.New("automatic schema downgrade is disabled")
}

func (s *checksumStore) verify(ctx context.Context, db database.DBTxConn, version int64) error {
	if version == 0 {
		return nil
	}
	source, ok := s.sources[version]
	if !ok {
		return fmt.Errorf("database migration %d is newer than this binary", version)
	}
	var name, sum string
	err := db.QueryRowContext(ctx, "SELECT name,sha256 FROM sf_schema_migrations WHERE version=$1", version).Scan(&name, &sum)
	if err != nil {
		return fmt.Errorf("migration %d checksum unavailable: %w", version, err)
	}
	if name != source.Name || sum != source.SHA256 {
		return fmt.Errorf("migration %d checksum mismatch: embedded %s, database %s", version, source.SHA256, sum)
	}
	return nil
}

func (s *checksumStore) GetMigration(ctx context.Context, db database.DBTxConn, version int64) (*database.GetMigrationResult, error) {
	result, err := s.Store.GetMigration(ctx, db, version)
	if err == nil && result.IsApplied {
		err = s.verify(ctx, db, version)
	}
	return result, err
}

func (s *checksumStore) GetLatestVersion(ctx context.Context, db database.DBTxConn) (int64, error) {
	version, err := s.Store.GetLatestVersion(ctx, db)
	if err == nil {
		err = s.verify(ctx, db, version)
	}
	return version, err
}

func (s *checksumStore) ListMigrations(ctx context.Context, db database.DBTxConn) ([]*database.ListMigrationsResult, error) {
	migrations, err := s.Store.ListMigrations(ctx, db)
	if err != nil {
		return nil, err
	}
	for _, migration := range migrations {
		if migration.IsApplied {
			if err := s.verify(ctx, db, migration.Version); err != nil {
				return nil, err
			}
		}
	}
	return migrations, nil
}

func (s *Store) MigrationRecords(ctx context.Context) ([]MigrationRecord, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT version,name,sha256,applied_ms FROM sf_schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []MigrationRecord{}
	for rows.Next() {
		var record MigrationRecord
		if err := rows.Scan(&record.Version, &record.Name, &record.SHA256, &record.AppliedMS); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Version < records[j].Version })
	return records, rows.Err()
}

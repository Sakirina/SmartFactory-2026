package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/compatibility"
)

func migrationHandoff(t *testing.T, dsn, driver string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(3)
	}
	db.SetMaxIdleConns(1)
	s := &Store{DB: db, Driver: driver, Now: time.Now}
	provider, err := s.migrationProvider()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.UpTo(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateRiver(ctx); err != nil {
		t.Fatal(err)
	}
	// River has committed its own migrations; the next process encounters a
	// business bridge DDL failure before Goose can record version 4.
	if _, err = db.Exec("CREATE TABLE sf_tasks(id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(ctx); err == nil {
		t.Fatal("expected bridge DDL failure")
	}
	var gooseVersion, riverVersion, checks int
	for query, target := range map[string]*int{"SELECT MAX(version_id) FROM goose_db_version WHERE is_applied": &gooseVersion, "SELECT MAX(version) FROM river_migration": &riverVersion, "SELECT COUNT(*) FROM sf_schema_migrations": &checks} {
		if err = db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if gooseVersion != 3 || riverVersion != 8 || checks != 3 {
		t.Fatal(gooseVersion, riverVersion, checks)
	}
	if _, err = db.Exec("DROP TABLE sf_tasks"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err = Open(ctx, dsn, "migration-handoff", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.DB.SetMaxOpenConns(3)
	if driver == "sqlite" {
		s.DB.SetMaxOpenConns(1)
	}
	s.DB.SetMaxIdleConns(1)
	records, err := s.MigrationRecords(ctx)
	database := "sqlite"
	if driver == "pgx" {
		database = "postgres"
	}
	latest := compatibility.Current().Databases[database].MigrationMaximum
	if err != nil || int64(len(records)) != latest {
		t.Fatal(records, err)
	}
	t.Logf("River migrations 1..8 committed; failed Goose bridge left version/checksum at 3; next startup resumed version 4 through current manifest version %d and kept all original checksums", latest)
}
func TestRiverMigrationHandoffResumesSQLite(t *testing.T) {
	migrationHandoff(t, filepath.Join(t.TempDir(), "handoff.db"), "sqlite")
}
func TestPostgresRiverMigrationHandoffResumes(t *testing.T) {
	dsn, _ := testdb.Postgres(t, "handoff")
	migrationHandoff(t, dsn, "pgx")
}

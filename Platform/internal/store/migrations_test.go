package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

func TestNumberedMigrationsAdoptLegacySQLiteAndPreserveHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := migrationFiles.ReadFile("migrations/sqlite/00001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.ReplaceAll(string(baseline), ",expires_ms BIGINT NOT NULL DEFAULT 0", "")
	if _, err := db.ExecContext(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	original := `{"id":"legacy","version":3,"counter":9007199254740993}`
	for _, query := range []string{
		`INSERT INTO documents VALUES('entity','legacy',3,123,$1)`,
		`INSERT INTO document_versions VALUES('entity','legacy',3,123,$1)`,
		`INSERT INTO document_versions VALUES('entity','missing-sync',1,124,$1)`,
		`INSERT INTO sync_changes VALUES(21,'entity','legacy',3,123,$1)`,
	} {
		if _, err := db.ExecContext(ctx, query, original); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO inbox VALUES('old-message','digest','edge',99)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for attempt := 0; attempt < 2; attempt++ {
		s, err := Open(ctx, path, "cloud", make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := s.Get(ctx, "entity", "legacy")
		if err != nil || doc.Version != 3 || string(doc.Data) != original {
			t.Fatalf("legacy document changed: %+v %v", doc, err)
		}
		var expiry, received, syncCount, highest int64
		if err := s.DB.QueryRowContext(ctx, "SELECT received_ms,expires_ms FROM inbox WHERE id='old-message'").Scan(&received, &expiry); err != nil || received != 99 || expiry != 0 {
			t.Fatalf("legacy inbox: received=%d expiry=%d %v", received, expiry, err)
		}
		if err := s.DB.QueryRowContext(ctx, "SELECT count(*),MAX(sequence) FROM sync_changes").Scan(&syncCount, &highest); err != nil || syncCount != 2 || highest != 22 {
			t.Fatalf("sync history changed: count=%d sequence=%d %v", syncCount, highest, err)
		}
		records, err := s.MigrationRecords(ctx)
		manifest, manifestErr := MigrationManifest("sqlite")
		if err != nil || manifestErr != nil || len(records) != len(manifest) {
			t.Fatalf("migration records: %+v %v %v", records, err, manifestErr)
		}
		for i, record := range records {
			if record.Version != manifest[i].Version || record.Name != manifest[i].Name || record.SHA256 != manifest[i].SHA256 || record.AppliedMS <= 0 {
				t.Fatalf("migration provenance: %+v", record)
			}
		}
		s.Close()
	}
}

func TestNumberedMigrationsRejectChangedAndMissingChecksums(t *testing.T) {
	for _, change := range []struct{ sql, message string }{
		{"UPDATE sf_schema_migrations SET sha256='changed' WHERE version=1", "checksum mismatch"},
		{"DELETE FROM sf_schema_migrations WHERE version=2", "checksum unavailable"},
		{"INSERT INTO goose_db_version(version_id,is_applied) VALUES(99,true)", "newer than this binary"},
	} {
		t.Run(change.message, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "migrations.db")
			s, err := Open(ctx, path, "cloud", make([]byte, 32))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Put(ctx, "retained", "record", 0, map[string]string{"value": "preserved"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.ExecContext(ctx, change.sql); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if reopened, err := Open(ctx, path, "cloud", make([]byte, 32)); err == nil || !strings.Contains(err.Error(), change.message) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("unverified migration accepted: %v", err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var records int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM documents WHERE kind='retained'").Scan(&records); err != nil || records != 1 {
				t.Fatalf("failed startup changed user data: %d %v", records, err)
			}
		})
	}
}

func TestMigrationFailureRollsBackDDLVersionAndChecksum(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	base, err := database.NewStore(database.DialectSQLite3, "goose_db_version")
	if err != nil {
		t.Fatal(err)
	}
	checks := &checksumStore{Store: base, now: time.Now, sources: map[int64]MigrationRecord{1: {Version: 1, Name: "00001_failure.sql", SHA256: "fixture"}}}
	files := fstest.MapFS{"00001_failure.sql": {Data: []byte("-- +goose Up\nCREATE TABLE transient_data(id INTEGER);\nINSERT INTO absent_table VALUES(1);\n")}}
	provider, err := goose.NewProvider(goose.DialectCustom, db, files, goose.WithStore(checks), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(context.Background()); err == nil {
		t.Fatal("expected migration failure")
	}
	for _, query := range []string{"SELECT count(*) FROM sqlite_master WHERE name='transient_data'", "SELECT count(*) FROM goose_db_version WHERE version_id=1", "SELECT count(*) FROM sf_schema_migrations"} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed migration was partially recorded: %s count=%d %v", query, count, err)
		}
	}
	// Retrying the corrected, unapplied migration is allowed and records its
	// contents only after the DDL has committed successfully.
	files["00001_failure.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE transient_data(id INTEGER);\n")}
	provider, err = goose.NewProvider(goose.DialectCustom, db, files, goose.WithStore(checks), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresNumberedMigrationsPreserveExistingSchema(t *testing.T) {
	dsn := os.Getenv("SF_TEST_POSTGRES_DATABASE")
	if dsn == "" {
		t.Skip("set SF_TEST_POSTGRES_DATABASE to an isolated PostgreSQL fixture")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := fmt.Sprintf("sf_migration_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	legacy, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := migrationFiles.ReadFile("migrations/postgres/00001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, strings.ReplaceAll(string(baseline), ",expires_ms BIGINT NOT NULL DEFAULT 0", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, "INSERT INTO inbox VALUES('legacy','digest','edge',42)"); err != nil {
		t.Fatal(err)
	}
	original := `{"id":"legacy","version":3,"counter":9007199254740993}`
	for _, query := range []string{
		`INSERT INTO documents VALUES('entity','legacy',3,123,$1)`,
		`INSERT INTO document_versions VALUES('entity','legacy',3,123,$1)`,
		`INSERT INTO document_versions VALUES('entity','missing-sync',1,124,$1)`,
		`INSERT INTO sync_changes VALUES(21,'entity','legacy',3,123,$1)`,
		`INSERT INTO outbox VALUES('pending','edge_definition','edge',$1,123,2,456,'retry')`,
		`INSERT INTO audit VALUES('source',9,123,124,'request','previous','hash',$1)`,
	} {
		if _, err := legacy.ExecContext(ctx, query, original); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE observations_legacy PARTITION OF observations FOR VALUES FROM (0) TO (1000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `INSERT INTO observations VALUES('old-point',123,'old-message','device','temperature',124,1,'good','',$1)`, original); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	for attempt := 0; attempt < 2; attempt++ {
		s, err := Open(ctx, u.String(), "cloud", make([]byte, 32))
		if err != nil {
			t.Fatal(err)
		}
		records, err := s.MigrationRecords(ctx)
		manifest, manifestErr := MigrationManifest("pgx")
		if err != nil || manifestErr != nil || len(records) != len(manifest) {
			t.Fatalf("PostgreSQL migrations: %+v %v", records, err)
		}
		var expiry int64
		if err := s.DB.QueryRowContext(ctx, "SELECT expires_ms FROM inbox WHERE id='legacy'").Scan(&expiry); err != nil || expiry != 0 {
			t.Fatalf("PostgreSQL legacy inbox: %d %v", expiry, err)
		}
		for _, query := range []string{
			`SELECT data FROM documents WHERE kind='entity' AND id='legacy' AND version=3`,
			`SELECT data FROM document_versions WHERE kind='entity' AND id='legacy' AND version=3`,
			`SELECT payload FROM outbox WHERE id='pending' AND attempts=2 AND next_ms=456`,
			`SELECT data FROM audit WHERE source_id='source' AND sequence=9 AND previous_hash='previous' AND hash='hash'`,
			`SELECT data FROM observations_legacy WHERE id='old-point' AND observed_ms=123`,
		} {
			var stored string
			if err := s.DB.QueryRowContext(ctx, query).Scan(&stored); err != nil || stored != original {
				t.Fatalf("PostgreSQL legacy row changed: %s %q %v", query, stored, err)
			}
		}
		var count, highest int64
		if err := s.DB.QueryRowContext(ctx, "SELECT count(*),MAX(sequence) FROM sync_changes").Scan(&count, &highest); err != nil || count != 2 || highest != 22 {
			t.Fatalf("PostgreSQL sync history changed: count=%d sequence=%d %v", count, highest, err)
		}
		s.Close()
	}
}

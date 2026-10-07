package store

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/pkg/model"
)

func upgradePoints() []model.Observation {
	points := []model.Observation{}
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC).UnixMilli()
	for i := 0; i < 128; i++ {
		points = append(points, model.Observation{ID: fmt.Sprintf("upgrade-point-%03d", i), OriginID: fmt.Sprintf("original-%03d", i), MessageID: fmt.Sprintf("message-%03d", i), SourceID: "legacy-edge", SourceSequence: uint64(9007199254740993 + i), DeviceID: "legacy-device", Key: "energy", Value: json.Number("9007199254740993"), ObservedMS: base + int64(i)*60000, ReceivedMS: base + int64(i)*60000 + 10, Unit: "kWh", Quality: "UNCERTAIN", QualityReason: "retained fixture", TimeSource: "device", EntityRevision: 7, AssetVersion: 4, RuleVersion: 3, Revision: 2, Late: true})
	}
	return points
}

// Invoked by the controlled Docker pg_dump/pg_restore procedure, using two
// exclusive fixture databases. Normal test suites do not create containers.
func TestControlledPostgresUpgradeFixture(t *testing.T) {
	phase, dsn := os.Getenv("SF_UPGRADE_PHASE"), os.Getenv("SF_UPGRADE_DATABASE")
	if phase == "" || dsn == "" {
		t.Skip("controlled PostgreSQL 16 -> 18 fixture")
	}
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	points := upgradePoints()
	original := `{"id":"legacy-device","version":3,"kind":"device","name":"Legacy","attributes":{"counter":9007199254740993}}`
	if phase == "seed" {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(3)
		baseline, err := migrationFiles.ReadFile("migrations/postgres/00001_baseline.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, strings.ReplaceAll(string(baseline), ",expires_ms BIGINT NOT NULL DEFAULT 0", "")); err != nil {
			t.Fatal(err)
		}
		seed := sha256.Sum256(append(make([]byte, 32), []byte("audit/migration-node")...))
		s := &Store{DB: db, Driver: "pgx", NodeID: "migration-node", SignKey: ed25519.NewKeyFromSeed(seed[:]), Now: func() time.Time { return now }, partitions: map[string]bool{}}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		legacy := &Tx{Tx: tx, Store: s, Ctx: ctx}
		if _, err = tx.ExecContext(ctx, "INSERT INTO documents VALUES('entity','legacy-device',3,123,$1)", original); err != nil {
			t.Fatal(err)
		}
		for version := 1; version <= 3; version++ {
			if _, err = tx.ExecContext(ctx, "INSERT INTO document_versions VALUES('entity','legacy-device',$1,$2,$3)", version, 120+version, original); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO sync_changes VALUES(21,'entity','legacy-device',3,123,$1)", original); err != nil {
			t.Fatal(err)
		}
		job, _ := json.Marshal(model.Job{ID: "legacy-recompute", Kind: "recompute", Status: "pending", DeviceID: "legacy-device", FromMS: points[0].ObservedMS, ToMS: points[127].ObservedMS, CursorMS: points[31].ObservedMS, Progress: .25, Version: 1})
		if _, err = tx.ExecContext(ctx, "INSERT INTO documents VALUES('job','legacy-recompute',1,123,$1)", string(job)); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO document_versions VALUES('job','legacy-recompute',1,123,$1)", string(job)); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO inbox VALUES('legacy-message','legacy-digest','legacy-edge',123)"); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO outbox VALUES('legacy-pending','tb_entity','legacy-device',$1,123,2,456,'temporary error')", original); err != nil {
			t.Fatal(err)
		}
		block, err := encodeArchive(points[:64])
		if err != nil {
			t.Fatal(err)
		}
		if err = legacy.saveArchive(block); err != nil {
			t.Fatal(err)
		}
		for _, point := range points[64:] {
			if err = legacy.InsertPoint(point); err != nil {
				t.Fatal(err)
			}
		}
		for index := 0; index < 3; index++ {
			if err = legacy.Audit(model.Actor{UserID: "legacy-operator"}, "migration.fixture", "legacy-device", fmt.Sprint(index), map[string]any{"counter": json.Number("9007199254740993")}); err != nil {
				t.Fatal(err)
			}
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var version string
		if err = db.QueryRow("SHOW server_version").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(version, "16.") {
			t.Fatal(version)
		}
		t.Logf("seed PostgreSQL %s: unnumbered legacy schema without inbox expiry; entity+3 versions; pending job; outbox attempts=2; sync cursor=21; 3 signed audit records; 64 hot +64 archived exact observations hash=%s", version, Hash(points))
		return
	}
	if phase != "verify" {
		t.Fatal("unsupported fixture phase")
	}
	s, err := Open(ctx, dsn, "migration-node", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.DB.SetMaxOpenConns(3)
	s.Now = func() time.Time { return now }
	var version string
	if err = s.DB.QueryRow("SHOW server_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(version, "18.") {
		t.Fatal(version)
	}
	doc, err := s.Get(ctx, "entity", "legacy-device")
	if err != nil || doc.Version != 3 || string(doc.Data) != original {
		t.Fatal(doc, err)
	}
	history, err := s.Versions(ctx, "entity", "legacy-device")
	if err != nil || len(history) != 3 {
		t.Fatal(history, err)
	}
	for _, item := range history {
		if string(item.Data) != original {
			t.Fatal(item)
		}
	}
	query, err := s.Query(ctx, Query{DeviceIDs: []string{"legacy-device"}, RawOnly: true, FromMS: points[0].ObservedMS, ToMS: points[127].ObservedMS, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(query.Points, func(i, j int) bool { return query.Points[i].ID < query.Points[j].ID })
	if Hash(query.Points) != Hash(points) {
		t.Fatalf("hot+archive fields changed: count=%d hash=%s expected=%s", len(query.Points), Hash(query.Points), Hash(points))
	}
	issues, err := s.VerifyAudit(ctx)
	if err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
	changes, err := s.Changes(ctx, 20, 100)
	if err != nil || len(changes) != 3 || changes[0].Sequence != 21 || changes[0].Document.Version != 3 {
		t.Fatal(changes, err)
	}
	delivery, err := s.Delivery(ctx, "legacy-pending")
	if err != nil || delivery.Attempts != 2 || delivery.NextMS != 456 || string(delivery.Payload) != original {
		t.Fatal(delivery, err)
	}
	var expiry int64
	if err = s.DB.QueryRow("SELECT expires_ms FROM inbox WHERE id='legacy-message'").Scan(&expiry); err != nil || expiry != 0 {
		t.Fatal(expiry, err)
	}
	records, err := s.MigrationRecords(ctx)
	if err != nil || len(records) != 4 {
		t.Fatal(records, err)
	}
	if err = s.ReconcileTasks(ctx); err != nil {
		t.Fatal(err)
	}
	jobDoc, err := s.Get(ctx, "job", "legacy-recompute")
	job, _ := Decode[model.Job](jobDoc)
	if err != nil || job.TaskID == "" || job.CursorMS != points[31].ObservedMS {
		t.Fatal(job, err)
	}
	if _, err = s.Task(ctx, job.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Task(ctx, "tb_entity:legacy-pending"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put(ctx, "entity", "post-upgrade", 0, model.Entity{ID: "post-upgrade", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err = s.Audit(ctx, model.Actor{UserID: "upgrade-operator"}, "upgrade.write", "post-upgrade", "new-audit", map[string]int{"ok": 1}); err != nil {
		t.Fatal(err)
	}
	issues, err = s.VerifyAudit(ctx)
	if err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
	stats, err := s.ArchiveObservations(ctx)
	if err != nil || stats.Points != 64 {
		t.Fatal(stats, err)
	}
	query, err = s.Query(ctx, Query{DeviceIDs: []string{"legacy-device"}, RawOnly: true, FromMS: points[0].ObservedMS, ToMS: points[127].ObservedMS, Limit: 200})
	sort.Slice(query.Points, func(i, j int) bool { return query.Points[i].ID < query.Points[j].ID })
	if err != nil || Hash(query.Points) != Hash(points) {
		t.Fatal("post-upgrade archive changed data", err)
	}
	t.Logf("verified PostgreSQL %s: legacy documents/history exact; 128 observations all fields exact hash=%s; signed audit chain 3 ->4; old cursor=21 preserved plus2 repaired entries; pending outbox and job adopted into River; migrations=4; new read/write/archive=64 pass", version, Hash(query.Points))
}

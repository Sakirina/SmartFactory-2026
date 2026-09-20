package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"testing"
	"time"

	"competition2026/product/platform/pkg/model"
)

func TestPostgresArchiveCapacityAndLatePartitionRecovery(t *testing.T) {
	dsn := os.Getenv("SF_TEST_POSTGRES_DATABASE")
	if dsn == "" {
		t.Skip("isolated PostgreSQL fixture required")
	}
	ctx := context.Background()
	admin, err := Open(ctx, dsn, "archive-fixture", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	const schema = "sf_archive_fixture"
	if _, err = admin.DB.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec("DROP SCHEMA " + schema + " CASCADE")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := Open(ctx, u.String(), "archive-fixture", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	base := now.Add(-48 * time.Hour).UnixMilli()
	points := make([]model.Observation, 8192)
	records := make([][]any, 0, len(points))
	for i := range points {
		hash := sha256.Sum256([]byte(fmt.Sprintf("fixed-seed/731/%d", i)))
		id := hex.EncodeToString(hash[:])
		p := archivePoint(id, base+int64(i/4)*1000)
		p.DeviceID = fmt.Sprintf("sensor-%d", i%4)
		p.SourceSequence = uint64(i)
		p.Value = float64((i*8179)%1000000) / 1000
		p.Quality = "GOOD"
		p.QualityReason = ""
		p.Late = false
		p.Unit = "degC"
		if i%97 == 0 {
			p.Quality = "BAD"
			p.QualityReason = "BadSensorFailure"
		}
		points[i] = p
		data, _ := json.Marshal(p)
		records = append(records, []any{p.ID, p.ObservedMS, p.MessageID, p.DeviceID, p.Key, p.ReceivedMS, p.Revision, p.Quality, p.DefinitionID, string(data)})
	}
	if err = s.Write(ctx, func(tx *Tx) error {
		if err := tx.EnsureDay(base); err != nil {
			return err
		}
		return tx.insertRows("observations", "id,observed_ms,message_id,device_id,key,received_ms,revision,quality,definition_id,data", "", records)
	}); err != nil {
		t.Fatal(err)
	}
	var hotBytes, coldBytes, coldCount, payloadBytes int64
	if err = s.DB.QueryRow("SELECT pg_total_relation_size('observations_20260918')").Scan(&hotBytes); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	stats, err := s.ArchiveObservations(ctx)
	elapsed := time.Since(started)
	if err != nil || stats.Points != int64(len(points)) {
		t.Fatal(stats, err)
	}
	if err = s.DB.QueryRow("SELECT pg_total_relation_size('observation_archives')").Scan(&coldBytes); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow("SELECT sum(point_count),sum(octet_length(payload)) FROM observation_archives").Scan(&coldCount, &payloadBytes); err != nil {
		t.Fatal(err)
	}
	var partitions int
	if err = s.DB.QueryRow("SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND tablename='observations_20260918'").Scan(&partitions); err != nil || partitions != 0 {
		t.Fatal("archived partition not retired", partitions, err)
	}
	got, err := s.Query(ctx, Query{FromMS: base, ToMS: now.UnixMilli(), Limit: 10000})
	if err != nil {
		t.Fatal(err)
	}
	sameArchivePoints(t, points, got.Points)
	// A late observation must recreate the retired day and merge with its archive.
	late := archivePoint("late-after-archive", base+1)
	insertArchivePoints(t, s, late)
	got, err = s.Query(ctx, Query{FromMS: base, ToMS: now.UnixMilli(), Limit: 10000})
	if err != nil || len(got.Points) != len(points)+1 {
		t.Fatal(len(got.Points), err)
	}
	if stats, err = s.ArchiveObservations(ctx); err != nil || stats.Points != 1 {
		t.Fatal(stats, err)
	}
	cutoff := time.UnixMilli(base+1000000).AddDate(0, 0, 30)
	s.Now = func() time.Time { return cutoff }
	counts, err := s.ApplyRetention(ctx, DefaultRetention())
	if err != nil || counts["archived_raw"] != 4001 {
		t.Fatal(counts, err)
	}
	got, err = s.Query(ctx, Query{FromMS: base, ToMS: now.UnixMilli(), Limit: 10000})
	if err != nil || len(got.Points) != 4192 {
		t.Fatal(len(got.Points), err)
	}
	report := map[string]any{"status": "passed", "fixture": "8192 deterministic observations; 4 series; SHA256 source identifiers; numeric values; BAD quality samples; 48 hours old", "seed": 731, "runtime": runtime.GOOS + "/" + runtime.GOARCH, "go_version": runtime.Version(), "postgres_storage": "caller-supplied isolated schema", "points": coldCount, "hot_table_and_indexes_bytes": hotBytes, "archive_table_toast_and_indexes_bytes": coldBytes, "gzip_payload_bytes": payloadBytes, "archive_bytes_per_point": float64(coldBytes) / float64(coldCount), "hot_bytes_per_point": float64(hotBytes) / float64(coldCount), "elapsed_ms": elapsed.Milliseconds(), "hot_day_partition_retired": true, "full_fields_recovered": true, "late_partition_recreated": true, "partial_archive_retention_removed": counts["archived_raw"], "retained_after_cutoff": len(got.Points)}
	raw, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(raw))
	if output := os.Getenv("SF_ARCHIVE_REPORT"); output != "" {
		if err = os.WriteFile(output, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

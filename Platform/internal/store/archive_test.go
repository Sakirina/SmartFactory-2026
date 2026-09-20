package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/pkg/model"
)

func archivePoint(id string, at int64) model.Observation {
	return model.Observation{ID: id, OriginID: "origin:" + id, MessageID: "message:" + id, SourceID: "edge-a", SourceSequence: 18446744073709551615, DeviceID: "sensor", Key: "value", Value: json.Number("18446744073709551615"), ObservedMS: at, ReceivedMS: at + 17000, Quality: "BAD", QualityReason: "BadSensorFailure", TimeSource: "device", Unit: "counts", EntityRevision: 7, AssetVersion: 4, Late: true, Revision: 1}
}
func insertArchivePoints(t *testing.T, s *Store, points ...model.Observation) {
	t.Helper()
	if err := s.Write(context.Background(), func(tx *Tx) error {
		for _, p := range points {
			if err := tx.InsertPoint(p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func sameArchivePoints(t *testing.T, want, got []model.Observation) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("points: want %d got %d", len(want), len(got))
	}
	expected := map[string]string{}
	for _, p := range want {
		b, _ := json.Marshal(p)
		expected[p.ID] = string(b)
	}
	for _, p := range got {
		b, _ := json.Marshal(p)
		if expected[p.ID] != string(b) {
			t.Fatalf("changed observation %s: %s", p.ID, b)
		}
	}
}
func TestArchivePreservesFullObservationsRevisionsAndRollups(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	base := s.Now().Add(-48 * time.Hour).UnixMilli()
	points := make([]model.Observation, 0, 4325)
	for i := 0; i < 4321; i++ {
		p := archivePoint(fmt.Sprintf("raw-%05d", i), base+int64(i))
		if i%3 != 0 {
			p.Quality = "GOOD"
			p.QualityReason = ""
		}
		points = append(points, p)
	}
	// Independent raw events at the same timestamp must remain independent.
	other := archivePoint("raw-same-time", base)
	points = append(points, other)
	d := archivePoint("derived-old", base)
	d.Key = "mean"
	d.DefinitionID = "analysis"
	d.RuleVersion = 2
	d.Quality = "GOOD"
	d.Value = 1
	points = append(points, d)
	insertArchivePoints(t, s, points...)
	query := Query{DeviceIDs: []string{"sensor"}, FromMS: base, ToMS: base + 10000, Limit: 10000}
	before, err := s.Query(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := s.ArchiveObservations(ctx)
	if err != nil || stats.Points != int64(len(points)) || stats.Blocks != 3 {
		t.Fatal(stats, err)
	}
	if stats.CompressedBytes >= stats.PlainBytes/3 {
		t.Fatal("unexpected fixture compression", stats)
	}
	var hot int
	if err = s.DB.QueryRow("SELECT count(*) FROM observations").Scan(&hot); err != nil || hot != 0 {
		t.Fatal(hot, err)
	}
	for _, want := range []model.Observation{points[0], points[len(points)-1]} {
		found, e := s.FindObservation(ctx, want.ID, want.ObservedMS, want.DeviceID, want.Key)
		if e != nil {
			t.Fatal(e)
		}
		sameArchivePoints(t, []model.Observation{want}, []model.Observation{found})
	}
	after, err := s.Query(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	sameArchivePoints(t, before.Points, after.Points)
	if before.Quality != after.Quality || before.DataVersion != after.DataVersion {
		t.Fatal("quality/version changed", before.Quality, after.Quality)
	}
	first, last, err := s.ObservationRange(ctx, []string{"sensor"}, true)
	if err != nil || first != base || last != base+4320 {
		t.Fatal(first, last, err)
	}
	d.ID = "derived-new"
	d.Revision = 2
	d.ReceivedMS = s.Now().UnixMilli()
	d.Value = 3
	insertArchivePoints(t, s, d)
	found, findErr := s.FindObservation(ctx, d.ID, d.ObservedMS, d.DeviceID, d.Key)
	if findErr != nil {
		t.Fatal(findErr)
	}
	sameArchivePoints(t, []model.Observation{d}, []model.Observation{found})
	after, err = s.Query(ctx, query)
	if err != nil || len(after.Points) != len(points) {
		t.Fatal(len(after.Points), err)
	}
	for _, p := range after.Points {
		if p.Key == "mean" && (p.ID != d.ID || fmt.Sprint(p.Value) != "3") {
			t.Fatal("wrong hot/cold revision", p)
		}
	}
	raw, err := s.Query(ctx, Query{DeviceIDs: []string{"sensor"}, FromMS: base, ToMS: base + 10000, RawOnly: true, Limit: 10000})
	if err != nil || len(raw.Points) != 4322 {
		t.Fatal(len(raw.Points), err)
	}
	limited, err := s.Query(ctx, Query{FromMS: base, ToMS: base + 10000, Limit: 10})
	if err != nil || !limited.Truncated || len(limited.Points) != 10 {
		t.Fatal(limited, err)
	}
	if err = s.BuildRollups(ctx, base, base+60000); err != nil {
		t.Fatal(err)
	}
	rolls, err := s.QueryRollups(ctx, Query{DeviceIDs: []string{"sensor"}, Keys: []string{"value"}, FromMS: base, ToMS: base + 60000, Resolution: "minute", Limit: 100})
	if err != nil || len(rolls.Points) != 1 {
		t.Fatal(rolls, err)
	}
	expected := AggregatePoints(raw.Points)
	b, _ := json.Marshal(expected)
	got, _ := json.Marshal(rolls.Points[0].Value)
	if string(b) != string(got) {
		t.Fatalf("rollup changed: %s / %s", b, got)
	}
}
func TestArchiveRollbackCorruptionAndRetention(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	base := s.Now().Add(-48 * time.Hour).UnixMilli()
	insertArchivePoints(t, s, archivePoint("one", base), archivePoint("two", base+1000), archivePoint("three", base+2000))
	if _, err := s.DB.Exec(`CREATE TRIGGER reject_archive BEFORE INSERT ON observation_archives BEGIN SELECT RAISE(ABORT,'storage full'); END`); err != nil {
		t.Fatal(err)
	}
	if stats, err := s.ArchiveObservations(ctx); err == nil || stats.Points != 0 {
		t.Fatal("failed archive retired data", stats, err)
	}
	var count int
	s.DB.QueryRow("SELECT count(*) FROM observations").Scan(&count)
	if count != 3 {
		t.Fatal(count)
	}
	s.DB.Exec("DROP TRIGGER reject_archive")
	if _, err := s.ArchiveObservations(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE observation_archives SET sha256='tampered'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Query(ctx, Query{FromMS: base, ToMS: base + 2000}); err == nil {
		t.Fatal("corrupt archive was silently accepted")
	}
	if _, err := s.DB.Exec("UPDATE observation_archives SET sha256=id"); err != nil {
		t.Fatal(err)
	}
	// The retention cutoff intersects the middle of one compressed block.
	now := time.UnixMilli(base+1000).AddDate(0, 0, 30)
	s.Now = func() time.Time { return now }
	counts, err := s.ApplyRetention(ctx, DefaultRetention())
	if err != nil || counts["archived_raw"] != 1 {
		t.Fatal(counts, err)
	}
	q, err := s.Query(ctx, Query{FromMS: base, ToMS: base + 2000})
	if err != nil || len(q.Points) != 2 {
		t.Fatal(q, err)
	}
	now = now.Add(time.Minute)
	counts, err = s.ApplyRetention(ctx, DefaultRetention())
	if err != nil || counts["archived_raw"] != 2 {
		t.Fatal(counts, err)
	}
	q, err = s.Query(ctx, Query{FromMS: base, ToMS: base + 2000})
	if err != nil || len(q.Points) != 0 {
		t.Fatal(q, err)
	}
}
func TestArchiveSurvivesBackupAndRestore(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for backup tool")
	}
	dir := t.TempDir()
	s := testStoreAt(t, filepath.Join(dir, "source.db"))
	now := s.Now()
	base := now.Add(-48 * time.Hour).UnixMilli()
	p := archivePoint("exact-large-integer", base)
	insertArchivePoints(t, s, p)
	if _, err = s.ArchiveObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	script := filepath.Join("..", "..", "..", "scripts", "backup-state.py")
	snapshot := filepath.Join(dir, "backup")
	restored := filepath.Join(dir, "restored")
	for _, args := range [][]string{{script, "create", "--output", snapshot, "--sqlite", "store.db=" + filepath.Join(dir, "source.db"), "--quiesced"}, {script, "restore", snapshot, "--output", restored}} {
		if out, e := exec.Command(python, args...).CombinedOutput(); e != nil {
			t.Fatal(string(out), e)
		}
	}
	r := testStoreAt(t, filepath.Join(restored, "store.db"))
	q, err := r.Query(context.Background(), Query{FromMS: base, ToMS: base})
	if err != nil {
		t.Fatal(err)
	}
	sameArchivePoints(t, []model.Observation{p}, q.Points)
}

func TestArchiveColumnFormatPreservesVariableTypesAndIdentifiers(t *testing.T) {
	values := []any{json.Number("9007199254740993"), json.Number("18446744073709551615"), json.Number("0.0000000000001"), true, false, "temperature/中文", nil, map[string]any{"nested": []any{json.Number("9223372036854775807"), false, "x"}}}
	points := make([]model.Observation, 1000)
	for i := range points {
		p := archivePoint(fmt.Sprintf("frame-%d:0", i), 1700000000000+int64(i*137))
		p.MessageID = fmt.Sprintf("frame-%d", i)
		p.OriginID = p.ID
		p.Value = values[i%len(values)]
		p.ReceivedMS = p.ObservedMS + int64(i%7-3)
		p.SourceSequence = uint64(i) * 1_000_000_000_000_000
		if i%2 == 0 {
			p.Unit = ""
			p.QualityReason = ""
			p.DefinitionID = "calculate"
			p.RuleVersion = 4
			p.Revision = 2
		}
		if i%5 == 0 {
			p.OriginID = "independent-original"
		}
		points[i] = p
	}
	b, err := encodeArchive(points)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeArchive(b)
	if err != nil {
		t.Fatal(err)
	}
	sameArchivePoints(t, points, decoded)
}

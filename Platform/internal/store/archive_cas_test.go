package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/pkg/model"
)

func TestArchiveCandidatePreservesConcurrentLateDataAndRejectsChangedRows(t *testing.T) {
	for _, mode := range []string{"late", "changed", "new-block", "changed-block", "encode-failure", "commit-failure"} {
		t.Run(mode, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			base := s.Now().Add(-48 * time.Hour).UnixMilli()
			points := []model.Observation{archivePoint("one", base), archivePoint("two", base+1)}
			insertArchivePoints(t, s, points...)
			cut := s.Now().Add(-24 * time.Hour).UnixMilli()
			encoder := encodeArchive
			if mode == "encode-failure" {
				encoder = func([]model.Observation) (archiveBlock, error) {
					return archiveBlock{}, errors.New("injected compressor failure")
				}
			}
			candidate, err := s.prepareArchive(ctx, cut, s.Policy().Archive, encoder)
			if mode == "encode-failure" {
				if err == nil {
					t.Fatal("compression failure not returned")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "late":
				insertArchivePoints(t, s, archivePoint("late", base-1))
			case "changed":
				changed := points[0]
				changed.Value = json.Number("9007199254740995")
				raw, _ := json.Marshal(changed)
				if _, err = s.DB.Exec("UPDATE observations SET data=$1 WHERE id='one'", string(raw)); err != nil {
					t.Fatal(err)
				}
			case "new-block":
				if err = s.Write(ctx, func(tx *Tx) error { return tx.saveArchive(candidate.Block) }); err != nil {
					t.Fatal(err)
				}
			case "changed-block":
				if err = s.Write(ctx, func(tx *Tx) error { return tx.saveArchive(candidate.Block) }); err != nil {
					t.Fatal(err)
				}
				candidate, err = s.prepareArchive(ctx, cut, s.Policy().Archive, encoder)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.DB.Exec("UPDATE observation_archives SET payload=$1", []byte("changed during compression")); err != nil {
					t.Fatal(err)
				}
			case "commit-failure":
				_, err = s.DB.Exec(`CREATE TABLE commit_parent(id INTEGER PRIMARY KEY); CREATE TABLE commit_fault(parent_id INTEGER REFERENCES commit_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_at_commit AFTER INSERT ON observation_archives BEGIN INSERT INTO commit_fault VALUES(99); END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			err = s.commitArchive(ctx, candidate)
			var hot int
			if e := s.DB.QueryRow("SELECT COUNT(*) FROM observations").Scan(&hot); e != nil {
				t.Fatal(e)
			}
			if mode == "late" {
				if err != nil || hot != 1 {
					t.Fatal(hot, err)
				}
				result, e := s.Query(ctx, Query{FromMS: base - 1, ToMS: base + 2})
				if e != nil {
					t.Fatal(e)
				}
				sameArchivePoints(t, append(points, archivePoint("late", base-1)), result.Points)
			} else {
				if err == nil || hot != 2 {
					t.Fatal("candidate conflict retired original rows", mode, hot, err)
				}
				if mode != "commit-failure" && !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPostgresArchiveCASAndTransactionDuration(t *testing.T) {
	s := pgStore(t)
	ctx := context.Background()
	base := s.Now().Add(-48 * time.Hour).UnixMilli()
	const count = 1536
	records := []model.Observation{}
	for _, device := range []string{"legacy-duration", "prepared-duration"} {
		for i := 0; i < count; i++ {
			p := archivePoint(fmt.Sprintf("%s:%d", device, i), base+int64(i))
			p.DeviceID = device
			var value strings.Builder
			for block := 0; block < 24; block++ {
				sum := sha256.Sum256([]byte(fmt.Sprintf("fixed-seed/93/%d/%d", i, block)))
				value.WriteString(hex.EncodeToString(sum[:]))
			}
			p.Value = value.String()
			records = append(records, p)
		}
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		for _, p := range records {
			if err := tx.InsertPoint(p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Measured baseline retains the original read/encode/verify/delete algorithm
	// within the write transaction, on an equal-size independent series.
	started := time.Now()
	var legacyPlain int64
	if err := s.Write(ctx, func(tx *Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT data FROM observations WHERE device_id='legacy-duration' ORDER BY observed_ms,id")
		if err != nil {
			return err
		}
		points := []model.Observation{}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			var p model.Observation
			if err = DecodeJSON([]byte(raw), &p); err != nil {
				rows.Close()
				return err
			}
			points = append(points, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		block, err := encodeArchive(points)
		if err != nil {
			return err
		}
		if _, err = decodeArchive(block); err != nil {
			return err
		}
		legacyPlain = block.PlainBytes
		if err = tx.saveArchive(block); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM observations WHERE device_id='legacy-duration'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	legacyDuration := time.Since(started)
	policy := s.Policy().Archive
	policy.BlockPoints = count
	preparedAt := time.Now()
	candidate, err := s.prepareArchive(ctx, s.Now().Add(-24*time.Hour).UnixMilli(), policy, encodeArchive)
	preparation := time.Since(preparedAt)
	if err != nil || candidate == nil {
		t.Fatal(candidate, err)
	}
	// A late row lands after the candidate snapshot and is retained by exact CAS.
	late := archivePoint("late-pg", base-1)
	late.DeviceID = "prepared-duration"
	insertArchivePoints(t, s, late)
	started = time.Now()
	err = s.commitArchive(ctx, candidate)
	commitDuration := time.Since(started)
	totalDuration := time.Since(preparedAt)
	if err != nil {
		t.Fatal(err)
	}
	var hot int
	if err = s.DB.QueryRow("SELECT count(*) FROM observations").Scan(&hot); err != nil || hot != 1 {
		t.Fatal(hot, err)
	}
	result, err := s.Query(ctx, Query{FromMS: base - 1, ToMS: base + count, Limit: 4000})
	if err != nil || len(result.Points) != len(records)+1 {
		t.Fatal(len(result.Points), err)
	}
	sameArchivePoints(t, append(records, late), result.Points)
	t.Logf("equal series points=%d; legacy_plain_bytes=%d; new_plain_bytes=%d; legacy_write_transaction=%s; outside_preparation=%s; exact_CAS_write_transaction=%s; new_total_including_late_ingest=%s; concurrent_late_retained=1", count, legacyPlain, candidate.Block.PlainBytes, legacyDuration, preparation, commitDuration, totalDuration)
}

func TestPostgresArchiveRetirementAndCreationRace(t *testing.T) {
	s := pgStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	base := s.Now().Add(-48 * time.Hour).UnixMilli()
	first := archivePoint("old", base)
	insertArchivePoints(t, s, first)
	if _, err := s.ArchiveObservations(ctx); err != nil {
		t.Fatal(err)
	}
	// Both creators hold the same missing partition shared before requesting
	// creation. PostgreSQL resolves the upgrade conflict; Store does not replay.
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	name := "observations_" + time.UnixMilli(base).UTC().Format("20060102")
	for i := 0; i < 2; i++ {
		go func() {
			done <- s.Write(ctx, func(tx *Tx) error {
				if err := tx.lock("partition", name, true); err != nil {
					return err
				}
				ready <- struct{}{}
				<-release
				return tx.InsertPoint(archivePoint(fmt.Sprintf("recreate-%d", i), base+int64(i+1)))
			})
		}()
	}
	waitSignal(t, ready)
	waitSignal(t, ready)
	close(release)
	success, retry := 0, 0
	for i := 0; i < 2; i++ {
		err := <-done
		if err == nil {
			success++
		} else if errors.Is(err, ErrRetryable) {
			retry++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || retry != 1 {
		t.Fatal(success, retry)
	}
	if _, err := s.ArchiveObservations(ctx); err != nil {
		t.Fatal(err)
	}
	// Two independent retirees may both list the table before the winner drops
	// it. Rechecking its presence after the advisory lock makes both safe.
	if err := s.Write(ctx, func(tx *Tx) error { return tx.EnsureDay(base) }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		go func() { done <- s.retireEmptyArchivedDays(ctx, s.Now().UnixMilli()) }()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	insertArchivePoints(t, s, archivePoint("after-retire", base+10))
	t.Log("missing partition: one creator committed, one recognizable retryable deadlock; two retirees succeeded; later ingestion recreated partition")
}

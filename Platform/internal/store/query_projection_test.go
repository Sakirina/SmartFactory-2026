package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func queryFixture(t *testing.T, pg bool) *Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "queries.db")
	if pg {
		dsn, _ = testdb.Postgres(t, "queries")
	}
	s, err := Open(context.Background(), dsn, "query-test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.DB.SetMaxOpenConns(3)
	s.DB.SetMaxIdleConns(1)
	if !pg {
		s.DB.SetMaxOpenConns(1)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func queryPageTest(t *testing.T, s *Store, kind string, opts model.QueryRequest, pos QueryPosition) QueryReadResult {
	t.Helper()
	if opts.Limit == 0 {
		opts.Limit = 100
	}
	out, err := s.QueryPage(context.Background(), kind, opts, QueryScope{All: true, AuthRevision: -1}, pos)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func querySeed(t *testing.T, s *Store) {
	t.Helper()
	err := s.Write(context.Background(), func(tx *Tx) error {
		for _, e := range []model.Entity{{ID: "factory", Kind: "asset"}, {ID: "other", Kind: "asset"}, {ID: "device-a", Name: "A", Kind: "device", ParentID: "factory"}, {ID: "device-b", Name: "B", Kind: "device", ParentID: "other"}} {
			if _, err := tx.Put("entity", e.ID, 0, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func queryStoreSuite(t *testing.T, pg bool) {
	t.Run("snapshot-paging-filter-atomic-delete", func(t *testing.T) {
		s := queryFixture(t, pg)
		ctx := context.Background()
		querySeed(t, s)
		fixed := time.Now()
		s.Now = func() time.Time { return fixed }
		for i := 0; i < 7; i++ {
			id := fmt.Sprintf("machine-%02d", i)
			_, err := s.Put(ctx, "entity", id, 0, model.Entity{ID: id, Name: "Machine", Kind: "device", ParentID: "factory", Status: "approved"})
			if err != nil {
				t.Fatal(err)
			}
		}
		opts := model.QueryRequest{EntityKind: "device", Search: "machine", Status: "approved", Limit: 2}
		first := queryPageTest(t, s, "entities", opts, QueryPosition{})
		if len(first.Rows) != 2 || !first.HasMore {
			t.Fatal(first)
		}
		if err := s.Write(ctx, func(tx *Tx) error {
			if _, err := tx.Put("entity", "machine-00", 1, model.Entity{ID: "machine-00", Name: "Changed", Kind: "device", ParentID: "factory"}); err != nil {
				return err
			}
			if err := tx.Delete("entity", "machine-03"); err != nil {
				return err
			}
			_, err := tx.Put("entity", "machine-99", 0, model.Entity{ID: "machine-99", Name: "Machine", Kind: "device", Status: "approved"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		all := append([]model.QueryRow{}, first.Rows...)
		next := first
		for next.HasMore {
			last := next.Rows[len(next.Rows)-1]
			pos := first.Position
			pos.AfterID = last.ID
			pos.AfterMS = last.SortMS
			next = queryPageTest(t, s, "entities", opts, pos)
			all = append(all, next.Rows...)
		}
		ids := []string{}
		for _, r := range all {
			ids = append(ids, r.ID)
		}
		want := []string{"machine-00", "machine-01", "machine-02", "machine-03", "machine-04", "machine-05", "machine-06"}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("snapshot pages: %v", ids)
		}
		current := queryPageTest(t, s, "entities", model.QueryRequest{EntityKind: "device", Search: "machine", Status: "approved", Limit: 100}, QueryPosition{})
		if len(current.Rows) != 6 {
			t.Fatalf("current count=%d", len(current.Rows))
		}
		state, _ := s.QueryState(ctx)
		scoped, err := s.QueryPage(ctx, "entities", model.QueryRequest{EntityKind: "device", Limit: 100}, QueryScope{Roots: []string{"factory"}, AuthRevision: state.AuthRevision}, QueryPosition{})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range scoped.Rows {
			if r.ID == "device-b" || r.ID == "machine-99" {
				t.Fatalf("scope leak %s", r.ID)
			}
		}
		var before int64
		_ = s.DB.QueryRow("SELECT head FROM sf_query_state").Scan(&before)
		failure := errors.New("rollback requested")
		err = s.Write(ctx, func(tx *Tx) error {
			_, e := tx.Put("entity", "rollback", 0, model.Entity{ID: "rollback", Kind: "device"})
			if e != nil {
				return e
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
		after, _ := s.QueryState(ctx)
		if after.Head != before {
			t.Fatal("rollback advanced query cursor")
		}
		t.Logf("stable tie ordering across seven rows, concurrent create/update/delete; authorized candidates %d; snapshot=%d current=%d", len(scoped.Rows), first.Position.Sequence, current.Position.Sequence)
	})
	t.Run("alarm-case-ephemeral-execution-evidence", func(t *testing.T) {
		s := queryFixture(t, pg)
		ctx := context.Background()
		querySeed(t, s)
		alarm := model.Alarm{ID: "alarm-a", EntityID: "device-a", DefinitionID: "rule", Active: true, UpdatedMS: 100, Version: 1, Value: json.Number("9007199254740993")}
		if err := s.Write(ctx, func(tx *Tx) error { return tx.SetEphemeral("alarm", alarm.ID, alarm) }); err != nil {
			t.Fatal(err)
		}
		first := queryPageTest(t, s, "alarms", model.QueryRequest{Limit: 10}, QueryPosition{})
		if _, err := s.Put(ctx, "alarm_case", alarm.ID, 0, map[string]any{"status": "conflict", "assignee_id": "operator", "acknowledged": true, "updated_ms": 200}); err != nil {
			t.Fatal(err)
		}
		ack := true
		filtered := queryPageTest(t, s, "alarms", model.QueryRequest{Acknowledged: &ack, AssigneeID: "operator", HandlingStatus: "conflict", Limit: 10}, QueryPosition{})
		if len(filtered.Rows) != 1 || filtered.Rows[0].Revision == first.Rows[0].Revision || !strings.Contains(string(filtered.Rows[0].Data), "9007199254740993") {
			t.Fatal(filtered)
		}
		alarm.Version++
		alarm.Count = 2
		alarm.Acknowledged = false
		if err := s.Write(ctx, func(tx *Tx) error { return tx.SetEphemeral("alarm", alarm.ID, alarm) }); err != nil {
			t.Fatal(err)
		}
		filtered = queryPageTest(t, s, "alarms", model.QueryRequest{Acknowledged: &ack, Limit: 10}, QueryPosition{})
		if len(filtered.Rows) != 1 {
			t.Fatal("lifecycle erased independent acknowledgement")
		}
		beforeNative := filtered.Rows[0].Revision
		if _, err := s.Put(ctx, "native_alarm_state", alarm.ID, 0, model.NativeAlarmState{ID: alarm.ID, NativeAlarmUpdate: model.NativeAlarmUpdate{SourceID: "native", NativeID: "native-id", SourceVersion: 8, Cleared: true}, Version: 1}); err != nil {
			t.Fatal(err)
		}
		native := queryPageTest(t, s, "alarms", model.QueryRequest{Limit: 10}, QueryPosition{})
		if native.Rows[0].Revision == beforeNative || !strings.Contains(string(native.Rows[0].Data), `"native_id":"native-id"`) || !strings.Contains(string(native.Rows[0].Data), `"active":true`) {
			t.Fatal("native update failed to revise query independently of rule active state", native)
		}
		if _, err := s.Put(ctx, "definition", "strategy", 0, model.Definition{ID: "strategy", GroupID: "factory", Version: 1, Policy: model.Policy{Steps: []model.Step{{DeviceID: "device-a"}}}}); err != nil {
			t.Fatal(err)
		}
		execution := model.Execution{DownlinkID: "run", DefinitionID: "strategy", DefinitionVersion: 1, Version: 1, Status: "completed", CreatedMS: 100}
		if _, err := s.Put(ctx, "execution", "run", 0, execution); err != nil {
			t.Fatal(err)
		}
		old := queryPageTest(t, s, "executions", model.QueryRequest{Limit: 10}, QueryPosition{})
		if err := s.Write(ctx, func(tx *Tx) error {
			return tx.SaveControlEvidence(model.CommandEvidence{ID: "evidence", ExecutionID: "run", DeviceID: "device-a"})
		}); err != nil {
			t.Fatal(err)
		}
		updated := queryPageTest(t, s, "executions", model.QueryRequest{Status: "completed", DefinitionID: "strategy", Limit: 10}, QueryPosition{})
		if updated.Rows[0].Version != 1 || updated.Rows[0].Revision == old.Rows[0].Revision || !strings.Contains(string(updated.Rows[0].Data), `"evidence_count":1`) {
			t.Fatal(updated)
		}
		head := updated.Position.Sequence
		if err := s.Write(ctx, func(tx *Tx) error {
			return tx.SaveControlEvidence(model.CommandEvidence{ID: "evidence", ExecutionID: "run", DeviceID: "device-a"})
		}); err != nil {
			t.Fatal(err)
		}
		state, _ := s.QueryState(ctx)
		if state.Head != head {
			t.Fatal("duplicate evidence advanced query revision")
		}
		t.Log("ephemeral lifecycle plus independent case; same-state evidence advances projection revision, duplicate evidence does not")
	})
	t.Run("trend-archive-rollup-revisions-rebuild", func(t *testing.T) {
		s := queryFixture(t, pg)
		ctx := context.Background()
		querySeed(t, s)
		now := time.Now().UTC()
		s.Now = func() time.Time { return now }
		at := now.Add(-48 * time.Hour).UnixMilli()
		points := []model.Observation{{ID: "raw-1", DeviceID: "device-a", Key: "total", Value: json.Number("9007199254740993"), SourceSequence: ^uint64(0), ObservedMS: at, ReceivedMS: at + 1, Quality: "GOOD", Unit: "count", Revision: 1}, {ID: "raw-2", DeviceID: "device-a", Key: "total", Value: json.Number("9007199254740995"), ObservedMS: at, ReceivedMS: at + 1, Quality: "BAD", Revision: 1}, {ID: "derived-v2", DeviceID: "device-a", Key: "derived", DefinitionID: "calculation", Value: json.Number("9007199254740999"), ObservedMS: at, ReceivedMS: at + 3, Quality: "GOOD", Revision: 2}, {ID: "derived-v1", DeviceID: "device-a", Key: "derived", DefinitionID: "calculation", Value: 1, ObservedMS: at, ReceivedMS: at + 2, Quality: "GOOD", Revision: 1}}
		if err := s.Write(ctx, func(tx *Tx) error {
			for _, p := range points {
				if err := tx.InsertPoint(p); err != nil {
					return err
				}
			}
			if _, err := tx.Put("revision", "r", 0, model.Revision{ID: "r", DeviceID: "device-a", Key: "derived", AtMS: at, Version: 2, Before: json.Number("9007199254740993"), After: json.Number("9007199254740999")}); err != nil {
				return err
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		opts := model.QueryRequest{ResourceIDs: []string{"device-a"}, FromMS: at - 1, ToMS: at + 60000, Resolution: "raw", Limit: 2}
		first := queryPageTest(t, s, "trend", opts, QueryPosition{})
		if len(first.Rows) != 2 || !first.HasMore || len(first.Metadata.Revisions) != 1 {
			t.Fatal(first)
		}
		full := queryPageTest(t, s, "trend", model.QueryRequest{ResourceIDs: opts.ResourceIDs, FromMS: opts.FromMS, ToMS: opts.ToMS, Resolution: "raw", Limit: 100}, QueryPosition{})
		if len(full.Rows) != 3 {
			t.Fatalf("expected two raw events and newest derived row, got %d", len(full.Rows))
		}
		if _, err := s.ArchiveObservations(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.BuildRollups(ctx, at, at+1); err != nil {
			t.Fatal(err)
		}
		rollup := queryPageTest(t, s, "trend", model.QueryRequest{FromMS: at / 60000 * 60000, ToMS: at + 60000, Resolution: "minute", Limit: 100}, QueryPosition{})
		if len(rollup.Rows) == 0 {
			t.Fatal("rollup projection missing")
		}
		start := time.Now()
		if err := s.RebuildQueryProjection(ctx); err != nil {
			t.Fatal(err)
		}
		rebuilt := queryPageTest(t, s, "trend", model.QueryRequest{ResourceIDs: opts.ResourceIDs, FromMS: opts.FromMS, ToMS: opts.ToMS, Resolution: "raw", Limit: 100}, QueryPosition{})
		for i, row := range full.Rows {
			if string(row.Data) != string(rebuilt.Rows[i].Data) || row.ID != rebuilt.Rows[i].ID {
				t.Fatalf("rebuild changed observation: %s", row.ID)
			}
		}
		if _, err := s.QueryPage(ctx, "trend", opts, QueryScope{All: true, AuthRevision: -1}, first.Position); !errors.Is(err, ErrQueryReset) {
			t.Fatal("old generation accepted", err)
		}
		if _, err := s.ApplyRetention(ctx, Retention{RawDays: 1, MinuteDays: 2, HourDays: 3, DayDays: 4, AlarmDays: 1, QuarantineDays: 1}); err != nil {
			t.Fatal(err)
		}
		expired := queryPageTest(t, s, "trend", model.QueryRequest{FromMS: at - 1, ToMS: at + 60000, Resolution: "raw", Limit: 100}, QueryPosition{})
		if len(expired.Rows) != 0 {
			t.Fatal("expired raw projection remains")
		}
		t.Logf("archive and rebuild retain exact integer, uint64 source sequence, raw duplicates, newest derived revision and rollup; rebuild %s", time.Since(start))
	})
	t.Run("failure-cancel-compaction-import", func(t *testing.T) {
		s := queryFixture(t, pg)
		ctx := context.Background()
		querySeed(t, s)
		old := queryPageTest(t, s, "entities", model.QueryRequest{Limit: 2}, QueryPosition{})
		payload := model.Entity{ID: "remote", Name: "new", Kind: "device", Version: 3}
		raw, _ := json.Marshal(payload)
		if err := s.Write(ctx, func(tx *Tx) error {
			_, err := tx.ImportDocument(Document{Kind: "entity", ID: payload.ID, Version: 3, UpdatedMS: 10, Data: raw})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		payload.Version = 2
		payload.Name = "stale"
		raw, _ = json.Marshal(payload)
		if err := s.Write(ctx, func(tx *Tx) error {
			_, err := tx.ImportDocument(Document{Kind: "entity", ID: payload.ID, Version: 2, UpdatedMS: 9, Data: raw})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		current := queryPageTest(t, s, "entities", model.QueryRequest{Search: "new", Limit: 100}, QueryPosition{})
		if len(current.Rows) != 1 || current.Rows[0].Version != 3 {
			t.Fatal(current)
		}
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		if err := s.RebuildQueryProjection(cancelCtx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		after, _ := s.QueryState(ctx)
		if after.Head != current.Position.Sequence {
			t.Fatal("cancelled rebuild advanced head")
		}
		if _, err := s.DB.ExecContext(ctx, "UPDATE documents SET data='invalid json' WHERE kind='entity' AND id='remote'"); err != nil {
			t.Fatal(err)
		}
		if err := s.RebuildQueryProjection(ctx); err == nil {
			t.Fatal("corrupt business input accepted")
		}
		after, _ = s.QueryState(ctx)
		if after.Epoch != current.Position.Epoch || after.Head != current.Position.Sequence {
			t.Fatal("failed rebuild replaced active generation")
		}
		if _, err := s.Put(ctx, "entity", "advance-floor", 0, model.Entity{ID: "advance-floor", Kind: "device"}); err != nil {
			t.Fatal(err)
		}
		if err := s.CompactQueryHistory(ctx, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := s.QueryPage(ctx, "entities", model.QueryRequest{Limit: 2}, QueryScope{All: true, AuthRevision: -1}, old.Position); !errors.Is(err, ErrQueryExpired) {
			t.Fatal("expired snapshot accepted", err)
		}
	})
}
func TestQueryProjectionSQLite(t *testing.T)   { queryStoreSuite(t, false) }
func TestQueryProjectionPostgres(t *testing.T) { queryStoreSuite(t, true) }

func TestQueryLateCommitAndConcurrentRebuildPostgres(t *testing.T) {
	s := queryFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	querySeed(t, s)
	for _, id := range []string{"early", "late"} {
		if _, err := s.Put(ctx, "entity", id, 0, model.Entity{ID: id, Kind: "device", Name: "original"}); err != nil {
			t.Fatal(err)
		}
	}
	ready := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Write(ctx, func(tx *Tx) error {
			if _, err := tx.Put("entity", "late", 1, model.Entity{ID: "late", Kind: "device", Name: "committed after rebuild"}); err != nil {
				return err
			}
			close(ready)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := s.Put(ctx, "entity", "early", 1, model.Entity{ID: "early", Kind: "device"}); err != nil {
		t.Fatal(err)
	}
	before := queryPageTest(t, s, "entities", model.QueryRequest{Limit: 100}, QueryPosition{})
	for _, r := range before.Rows {
		if r.ID == "late" && !strings.Contains(string(r.Data), "original") {
			t.Fatal("uncommitted row exposed")
		}
	}
	start := time.Now()
	if err := s.RebuildQueryProjection(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := s.QueryState(ctx)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after := queryPageTest(t, s, "entities", model.QueryRequest{Search: "late", Limit: 100}, QueryPosition{})
	if len(after.Rows) != 1 || after.Position.Sequence <= state.Head || after.Position.Epoch != state.Epoch {
		t.Fatal(after, state)
	}
	t.Logf("late prepared business write committed after atomic generation switch; rebuild=%s old=%d switched=%d late=%d", time.Since(start), before.Position.Sequence, state.Head, after.Position.Sequence)
}

func TestQueryRebuildPostgresCancellationAndBlockedWriter(t *testing.T) {
	s := queryFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.Write(ctx, func(tx *Tx) error {
		for i := 0; i < 400; i++ {
			id := fmt.Sprintf("rebuild-%04d", i)
			if _, err := tx.Put("entity", id, 0, model.Entity{ID: id, Kind: "device", Name: id}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := queryPageTest(t, s, "entities", model.QueryRequest{Limit: 500}, QueryPosition{})
	if _, err := s.DB.ExecContext(ctx, `CREATE FUNCTION query_rebuild_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.003); RETURN NEW; END $$; CREATE TRIGGER query_rebuild_pause BEFORE INSERT ON sf_query_rows FOR EACH ROW EXECUTE FUNCTION query_rebuild_pause()`); err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(ctx, 40*time.Millisecond)
	started := time.Now()
	err := s.RebuildQueryProjection(short)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mid-rebuild cancellation: %v", err)
	}
	after := queryPageTest(t, s, "entities", model.QueryRequest{Limit: 500}, QueryPosition{})
	if before.Position != after.Position || !reflect.DeepEqual(before.Rows, after.Rows) {
		t.Fatal("cancelled rebuild changed committed projection")
	}
	cancelledAfter := time.Since(started)
	if _, err = s.DB.ExecContext(ctx, `DROP TRIGGER query_rebuild_pause ON sf_query_rows; DROP FUNCTION query_rebuild_pause(); CREATE FUNCTION query_rebuild_block() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(7107001); RETURN NEW; END $$; CREATE TRIGGER query_rebuild_block BEFORE INSERT ON sf_query_rows FOR EACH ROW EXECUTE FUNCTION query_rebuild_block()`); err != nil {
		t.Fatal(err)
	}
	blocker, err := s.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err = blocker.ExecContext(ctx, "SELECT pg_advisory_lock(7107001)"); err != nil {
		t.Fatal(err)
	}
	defer blocker.ExecContext(context.Background(), "SELECT pg_advisory_unlock(7107001)")
	rebuilding := make(chan error, 1)
	go func() { rebuilding <- s.RebuildQueryProjection(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		var count int
		if err = blocker.QueryRowContext(ctx, "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND objid::bigint=7107001").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			waiting = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !waiting {
		t.Fatal("rebuild did not reach projection insertion")
	}
	writer := make(chan error, 1)
	go func() {
		_, e := s.Put(ctx, "entity", "after-rebuild", 0, model.Entity{ID: "after-rebuild", Kind: "device"})
		writer <- e
	}()
	select {
	case err = <-writer:
		t.Fatalf("writer finalized during atomic rebuild: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	var head int64
	if err = blocker.QueryRowContext(ctx, "SELECT head FROM sf_query_state WHERE singleton=1").Scan(&head); err != nil || head != before.Position.Sequence {
		t.Fatal("reader saw uncommitted generation", head, err)
	}
	release := time.Now()
	if _, err = blocker.ExecContext(ctx, "SELECT pg_advisory_unlock(7107001)"); err != nil {
		t.Fatal(err)
	}
	if err = <-rebuilding; err != nil {
		t.Fatal(err)
	}
	if err = <-writer; err != nil {
		t.Fatal(err)
	}
	final := queryPageTest(t, s, "entities", model.QueryRequest{Search: "after-rebuild", Limit: 10}, QueryPosition{})
	if len(final.Rows) != 1 || final.Position.Epoch == before.Position.Epoch {
		t.Fatal(final)
	}
	if _, err = s.DB.ExecContext(ctx, `DROP TRIGGER query_rebuild_block ON sf_query_rows; DROP FUNCTION query_rebuild_block()`); err != nil {
		t.Fatal(err)
	}
	t.Logf("400 rows: cancellation after %s retained old snapshot; concurrent writer waited across controlled 50ms barrier and committed within %s after release", cancelledAfter, time.Since(release))
}

func TestQueryAllKindsStableKeyset(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprintf("postgres=%t", pg), func(t *testing.T) {
			for _, kind := range []string{"entities", "alarms", "executions", "trend"} {
				t.Run(kind, func(t *testing.T) {
					s := queryFixture(t, pg)
					ctx := context.Background()
					fixed := time.UnixMilli(1000)
					s.Now = func() time.Time { return fixed }
					if _, err := s.Put(ctx, "definition", "paging-rule", 0, model.Definition{ID: "paging-rule", Version: 1, GroupID: "resource"}); err != nil {
						t.Fatal(err)
					}
					put := func(id string) error {
						return s.Write(ctx, func(tx *Tx) error {
							switch kind {
							case "entities":
								_, err := tx.Put("entity", id, 0, model.Entity{ID: id, Kind: "device", Name: "Paged"})
								return err
							case "alarms":
								return tx.SetEphemeral("alarm", id, model.Alarm{ID: id, EntityID: "resource", Severity: "critical", UpdatedMS: 1000})
							case "executions":
								_, err := tx.Put("execution", id, 0, model.Execution{DownlinkID: id, DefinitionID: "paging-rule", DefinitionVersion: 1, CreatedMS: 1000, Status: "completed"})
								return err
							case "trend":
								return tx.InsertPoint(model.Observation{ID: id, DeviceID: "resource", Key: "counter", ObservedMS: 1000, Value: json.Number("9007199254740993"), Quality: "GOOD"})
							}
							return nil
						})
					}
					for _, id := range []string{"tie-A", "tie-a", "tie-z", "tie-ä", "tie-中", "tie-9", "tie-0", "tie-Z"} {
						if err := put(id); err != nil {
							t.Fatal(err)
						}
					}
					opts := model.QueryRequest{Limit: 500}
					switch kind {
					case "entities":
						opts.EntityKind = "device"
					case "alarms":
						opts.Status = "critical"
					case "executions":
						opts.Status = "completed"
						opts.DefinitionID = "paging-rule"
					case "trend":
						opts.Resolution = "raw"
						opts.FromMS = 1
						opts.ToMS = 2000
						opts.Keys = []string{"counter"}
					}
					expected := queryPageTest(t, s, kind, opts, QueryPosition{})
					if len(expected.Rows) != 8 {
						t.Fatal(expected)
					}
					opts.Limit = 3
					first := queryPageTest(t, s, kind, opts, QueryPosition{})
					if err := put("tie-11-new-after-snapshot"); err != nil {
						t.Fatal(err)
					}
					got := append([]model.QueryRow{}, first.Rows...)
					page := first
					for page.HasMore {
						last := page.Rows[len(page.Rows)-1]
						pos := page.Position
						pos.AfterMS = last.SortMS
						pos.AfterID = last.ID
						page = queryPageTest(t, s, kind, opts, pos)
						got = append(got, page.Rows...)
					}
					if !reflect.DeepEqual(expected.Rows, got) {
						t.Fatalf("snapshot paging differs for %s: want %+v got %+v", kind, expected.Rows, got)
					}
					if kind != "trend" {
						want := []string{"tie-0", "tie-9", "tie-A", "tie-Z", "tie-a", "tie-z", "tie-ä", "tie-中"}
						for i, id := range want {
							if got[i].ID != id {
								t.Fatalf("cross-database byte order at %d: %s != %s", i, got[i].ID, id)
							}
						}
					}
				})
			}
		})
	}
}

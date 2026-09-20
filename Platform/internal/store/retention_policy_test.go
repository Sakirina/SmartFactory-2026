package store

import (
	"competition2026/product/platform/pkg/model"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPartialMinuteRebuildPreservesUnchangedObservations(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	minute := s.Now().UnixMilli() / 60000 * 60000
	for i, value := range []int64{10, 20, 30} {
		batch := sample(s, fmt.Sprint(i), value)
		batch.Points[0].ObservedMS = minute + int64(i)*10000
		if _, err := s.Ingest(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BuildRollups(ctx, minute, minute+60000); err != nil {
		t.Fatal(err)
	}
	if err := s.BuildRollups(ctx, minute+20000, minute+20001); err != nil {
		t.Fatal(err)
	}
	for _, grain := range []string{"minute", "hour", "day"} {
		var raw string
		if err := s.DB.QueryRowContext(ctx, "SELECT data FROM rollups WHERE device_id='counter-1' AND key='count' AND granularity=$1", grain).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var data struct {
			Aggregate Aggregate `json:"aggregate"`
		}
		if err := DecodeJSON([]byte(raw), &data); err != nil {
			t.Fatal(err)
		}
		if data.Aggregate.Count != 3 || data.Aggregate.Sum != "60" {
			t.Fatal("partial rebuild lost existing values", grain, data.Aggregate)
		}
	}
}

func TestRetentionTiersQuarantineAndPermanentHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := s.Now()
	for _, item := range []struct {
		grain string
		days  int
	}{{"minute", 90}, {"hour", 365}, {"day", 1095}} {
		for _, age := range []int{item.days - 1, item.days + 1} {
			if _, e := s.DB.Exec("INSERT INTO rollups(device_id,key,granularity,bucket_ms,data) VALUES('sensor','value',$1,$2,'{}')", item.grain, now.AddDate(0, 0, -age).UnixMilli()); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, alarm := range []model.Alarm{{ID: "old", UpdatedMS: now.AddDate(0, 0, -1096).UnixMilli()}, {ID: "recent", UpdatedMS: now.AddDate(0, 0, -1094).UnixMilli()}, {ID: "active", Active: true, UpdatedMS: now.AddDate(0, 0, -1096).UnixMilli()}} {
		if _, e := s.Put(ctx, "alarm", alarm.ID, 0, alarm); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Put(ctx, "quarantine", "expired", 0, map[string]any{"expires_ms": now.Add(-time.Millisecond).UnixMilli()}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Put(ctx, "quarantine", "pending", 0, map[string]any{"expires_ms": now.Add(7 * 24 * time.Hour).UnixMilli()}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Put(ctx, "definition", "permanent", 0, map[string]any{"version": 1}); e != nil {
		t.Fatal(e)
	}
	for _, age := range []int{60, 31, 29} {
		if _, e := s.DB.Exec("INSERT INTO engine_checkpoints(state_id,bucket_ms,at_ms,data) VALUES('counter',$1,$1,'{}')", now.AddDate(0, 0, -age).UnixMilli()); e != nil {
			t.Fatal(e)
		}
	}
	counts, e := s.ApplyRetention(ctx, DefaultRetention())
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"minute", "hour", "day", "alarm", "quarantine", "checkpoints"} {
		if counts[kind] != 1 {
			t.Fatal(kind, counts)
		}
	}
	for _, item := range []struct{ kind, id string }{{"alarm", "recent"}, {"alarm", "active"}, {"quarantine", "pending"}, {"definition", "permanent"}} {
		if _, e = s.Get(ctx, item.kind, item.id); e != nil {
			t.Fatal("retained document disappeared", item, e)
		}
	}
	if _, e = s.Get(ctx, "quarantine", "expired"); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if history, e := s.Versions(ctx, "quarantine", "expired"); e != nil || len(history) != 0 {
		t.Fatal("expired payload remains in history", e)
	}
	issues, e := s.VerifyAudit(ctx)
	if e != nil || len(issues) != 0 {
		t.Fatal("retention broke permanent audit", e, issues)
	}
}

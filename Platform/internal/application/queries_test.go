package application

import (
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func queryAppFixture(t *testing.T, postgres bool) (*Queries, identity.Principal) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "queries.db")
	if postgres {
		dsn, _ = testdb.Postgres(t, "query_application")
	}
	db, err := store.Open(context.Background(), dsn, "query-test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.DB.SetMaxOpenConns(3)
	db.DB.SetMaxIdleConns(1)
	if !postgres {
		db.DB.SetMaxOpenConns(1)
	}
	t.Cleanup(func() { db.Close() })
	auth := &identity.Manager{Store: db}
	q := &Queries{Store: db, Identity: auth, Control: &control.Service{Store: db, Identity: auth, Definitions: &engine.Service{Store: db}}, PollInterval: 10 * time.Millisecond}
	user := model.User{ID: "reader", Version: 1, Active: true, Roles: []string{"viewer"}, Resources: []string{"factory"}}
	if _, err = db.Put(context.Background(), "user", user.ID, 0, user); err != nil {
		t.Fatal(err)
	}
	for _, e := range []model.Entity{{ID: "factory", Kind: "asset"}, {ID: "other", Kind: "asset"}, {ID: "device-a", Name: "A", Kind: "device", ParentID: "factory"}, {ID: "device-b", Name: "B", Kind: "device", ParentID: "other"}} {
		if _, err = db.Put(context.Background(), "entity", e.ID, 0, e); err != nil {
			t.Fatal(err)
		}
	}
	return q, identity.Principal{User: user, Actor: model.Actor{UserID: user.ID, Source: "test"}}
}
func queryEvent(t *testing.T, sub *QuerySubscription, kind string) model.QueryEvent {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case e, ok := <-sub.Events:
			if !ok {
				t.Fatal("subscription closed")
			}
			if e.Type == kind {
				return e
			}
			if e.Type == "reset" {
				t.Fatalf("unexpected reset: %+v", e)
			}
		case <-deadline.C:
			t.Fatalf("waiting for %s", kind)
		}
	}
}
func awaitQuery(t *testing.T, q *Queries, want func(QuerySubscriptionStats) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if want(q.SubscriptionStats()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscription stats: %+v", q.SubscriptionStats())
}
func querySetEntity(t *testing.T, q *Queries, id, parent, name string) {
	t.Helper()
	ctx := context.Background()
	d, e := q.Store.Get(ctx, "entity", id)
	version := int64(0)
	if e == nil {
		version = d.Version
	} else if !errors.Is(e, store.ErrNotFound) {
		t.Fatal(e)
	}
	if _, e = q.Store.Put(ctx, "entity", id, version, model.Entity{ID: id, Kind: "device", ParentID: parent, Name: name}); e != nil {
		t.Fatal(e)
	}
}
func queryApplicationSuite(t *testing.T, pg bool) {
	t.Run("signed-paging-candidates-and-current-authorization", func(t *testing.T) {
		q, p := queryAppFixture(t, pg)
		ctx := context.Background()
		first, err := q.Page(ctx, p, "entities", model.QueryRequest{Limit: 1})
		if err != nil || !first.HasMore {
			t.Fatal(first, err)
		}
		next := model.QueryRequest{Limit: 1, PageToken: first.NextPageToken}
		if _, err = q.Page(ctx, p, "entities", next); err != nil {
			t.Fatal(err)
		}
		next.Search = "A"
		if _, err = q.Page(ctx, p, "entities", next); !errors.Is(err, store.ErrQueryChanged) {
			t.Fatalf("changed filter %v", err)
		}
		next.Search = ""
		next.PageToken += "x"
		if _, err = q.Page(ctx, p, "entities", next); err == nil {
			t.Fatal("tampered cursor accepted")
		}
		// A malformed forbidden payload proves SQL excludes it before decompression.
		if _, err = q.Store.DB.Exec("UPDATE sf_query_rows SET data=$1 WHERE kind='entities' AND id='device-b'", []byte("invalid compressed data")); err != nil {
			t.Fatal(err)
		}
		visible, err := q.Page(ctx, p, "entities", model.QueryRequest{Limit: 10})
		if err != nil || len(visible.Items) != 2 {
			t.Fatal(visible, err)
		}
		querySetEntity(t, q, "device-a", "other", "moved")
		next.PageToken = first.NextPageToken
		if _, err = q.Page(ctx, p, "entities", next); !errors.Is(err, store.ErrQueryAuthorization) {
			t.Fatalf("ancestry cursor %v", err)
		}
		t.Log("database candidates exclude unauthorized malformed rows; signed continuation rejects changed filter, signature and current ancestry")
	})
	t.Run("shared-window-deletion-and-restart-resume", func(t *testing.T) {
		q, p := queryAppFixture(t, pg)
		ctx := context.Background()
		opts := model.QueryRequest{Limit: 10, EntityKind: "device"}
		var subscribers []*QuerySubscription
		var mu sync.Mutex
		var wg sync.WaitGroup
		errs := make(chan error, 12)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sub, e := q.Subscribe(ctx, p, "entities", opts, "")
				if e != nil {
					errs <- e
					return
				}
				mu.Lock()
				subscribers = append(subscribers, sub)
				mu.Unlock()
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
		for _, sub := range subscribers {
			t.Cleanup(sub.Close)
			queryEvent(t, sub, "snapshot")
		}
		before := q.SubscriptionStats()
		if before.Groups != 1 || before.Subscribers != 12 || before.PageReads != 1 || before.ActiveLoops != 1 {
			t.Fatalf("sharing %+v", before)
		}
		querySetEntity(t, q, "device-a", "factory", "Changed")
		var cursor string
		for _, sub := range subscribers {
			e := queryEvent(t, sub, "delta")
			if len(e.Changes) != 1 || e.Changes[0].Operation != "upsert" {
				t.Fatal(e)
			}
			cursor = e.Cursor
		}
		after := q.SubscriptionStats()
		if after.PageReads-before.PageReads != 1 {
			t.Fatalf("per-client requery %+v -> %+v", before, after)
		}
		for _, sub := range subscribers {
			sub.Close()
		}
		awaitQuery(t, q, func(s QuerySubscriptionStats) bool { return s.ActiveLoops == 0 && s.Groups == 0 })
		querySetEntity(t, q, "device-a", "factory", "While disconnected")
		restarted := &Queries{Store: q.Store, Identity: q.Identity, Control: q.Control, PollInterval: 10 * time.Millisecond}
		resumed, err := restarted.Subscribe(ctx, p, "entities", opts, cursor)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(resumed.Close)
		change := queryEvent(t, resumed, "delta")
		if len(change.Changes) != 1 || !strings.Contains(string(change.Changes[0].Item.Data), "While disconnected") {
			t.Fatal(change)
		}
		alarm := model.Alarm{ID: "alarm", EntityID: "device-a", Active: true, UpdatedMS: 1}
		if _, err = q.Store.Put(ctx, "alarm", alarm.ID, 0, alarm); err != nil {
			t.Fatal(err)
		}
		alarmSub, err := restarted.Subscribe(ctx, p, "alarms", model.QueryRequest{Limit: 10}, "")
		if err != nil {
			t.Fatal(err)
		}
		defer alarmSub.Close()
		old := queryEvent(t, alarmSub, "snapshot")
		if err = q.Store.Write(ctx, func(tx *store.Tx) error { return tx.Delete("alarm", "alarm") }); err != nil {
			t.Fatal(err)
		}
		deleted := queryEvent(t, alarmSub, "delta")
		if len(deleted.Changes) != 1 || deleted.Changes[0].Operation != "remove" || deleted.Changes[0].Revision == old.Page.Items[0].Revision {
			t.Fatal(deleted)
		}
		t.Log("12 concurrent subscribers shared 1 initial read and 1 changed-window read; new hub recovered disconnected delta; delete emitted newer removal revision")
	})
	t.Run("identity-grant-session-reset-and-release", func(t *testing.T) {
		for _, kind := range []string{"user", "grant", "session", "ancestry", "rebuild", "expiry"} {
			t.Run(kind, func(t *testing.T) {
				q, p := queryAppFixture(t, pg)
				ctx := context.Background()
				if kind == "grant" {
					p.User.Resources = nil
					p.User.Teams = []string{"team"}
					if _, err := q.Store.Put(ctx, "user", p.User.ID, 1, p.User); err != nil {
						t.Fatal(err)
					}
					if _, err := q.Store.Put(ctx, "grant", "grant", 0, identity.Grant{ID: "grant", TeamID: "team", Resources: []string{"factory"}, Actions: []string{"read"}}); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "session" {
					p.SessionDocument = "session"
					p.SessionVersion = 1
					if _, err := q.Store.Put(ctx, "session", "session", 0, identity.Session{ID: "session", UserID: p.User.ID, ExpiresMS: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
						t.Fatal(err)
					}
				}
				sub, err := q.Subscribe(ctx, p, "entities", model.QueryRequest{Limit: 10}, "")
				if err != nil {
					t.Fatal(err)
				}
				defer sub.Close()
				snap := queryEvent(t, sub, "snapshot")
				switch kind {
				case "user":
					p.User.Active = false
					_, err = q.Store.Put(ctx, "user", p.User.ID, 1, p.User)
				case "grant":
					err = q.Store.Write(ctx, func(tx *store.Tx) error { return tx.Delete("grant", "grant") })
				case "session":
					err = q.Store.Write(ctx, func(tx *store.Tx) error { return tx.Delete("session", "session") })
				case "ancestry":
					querySetEntity(t, q, "device-a", "other", "moved")
				case "rebuild":
					err = q.Store.RebuildQueryProjection(ctx)
				case "expiry":
					sub.Close()
					awaitQuery(t, q, func(s QuerySubscriptionStats) bool { return s.ActiveLoops == 0 })
					for i := 0; i < 3; i++ {
						querySetEntity(t, q, "device-a", "factory", fmt.Sprint(i))
					}
					err = q.Store.CompactQueryHistory(ctx, 1)
					if err == nil {
						sub, err = q.Subscribe(ctx, p, "entities", model.QueryRequest{Limit: 10}, snap.Cursor)
						if sub != nil {
							defer sub.Close()
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				e := queryEvent(t, sub, "reset")
				if !e.Clear {
					t.Fatal(e)
				}
				awaitQuery(t, q, func(s QuerySubscriptionStats) bool { return s.Subscribers == 0 && s.Groups == 0 && s.ActiveLoops == 0 })
			})
		}
	})
	t.Run("bounded-slow-consumer-and-monotonic-resume", func(t *testing.T) {
		q, p := queryAppFixture(t, pg)
		q.Buffer = 1
		ctx := context.Background()
		opts := model.QueryRequest{Limit: 10}
		sub, err := q.Subscribe(ctx, p, "entities", opts, "")
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		querySetEntity(t, q, "device-a", "factory", "full queue")
		awaitQuery(t, q, func(s QuerySubscriptionStats) bool { return s.Subscribers == 0 && s.ActiveLoops == 0 })
		e := queryEvent(t, sub, "reset")
		if e.Reason != "slow_consumer" {
			t.Fatal(e)
		}
		q.PollInterval = time.Hour
		first, err := q.Subscribe(ctx, p, "entities", opts, "")
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		queryEvent(t, first, "snapshot")
		querySetEntity(t, q, "device-a", "factory", "new direct page")
		page, err := q.Page(ctx, p, "entities", opts)
		if err != nil {
			t.Fatal(err)
		}
		resumed, err := q.Subscribe(ctx, p, "entities", opts, page.SnapshotCursor)
		if err != nil {
			t.Fatal(err)
		}
		defer resumed.Close()
		event := queryEvent(t, resumed, "checkpoint")
		if event.Cursor != page.SnapshotCursor {
			t.Fatal("resume regressed behind provided cursor")
		}
		cancelCtx, cancel := context.WithCancel(ctx)
		cancellable, err := q.Subscribe(cancelCtx, p, "entities", opts, "")
		if err != nil {
			t.Fatal(err)
		}
		queryEvent(t, cancellable, "snapshot")
		cancel()
		first.Close()
		resumed.Close()
		awaitQuery(t, q, func(s QuerySubscriptionStats) bool { return s.Subscribers == 0 && s.ActiveLoops == 0 })
	})
	t.Run("execution-candidates-refresh-definition-and-evidence", func(t *testing.T) {
		q, p := queryAppFixture(t, pg)
		ctx := context.Background()
		def := model.Definition{ID: "strategy", Kind: "strategy", GroupID: "factory", Version: 1, Status: "published"}
		if _, err := q.Store.Put(ctx, "definition", def.ID, 0, def); err != nil {
			t.Fatal(err)
		}
		execution := model.Execution{DownlinkID: "run", DefinitionID: def.ID, DefinitionVersion: 1, Status: "completed", Version: 1}
		if _, err := q.Store.Put(ctx, "execution", execution.DownlinkID, 0, execution); err != nil {
			t.Fatal(err)
		}
		page, err := q.Page(ctx, p, "executions", model.QueryRequest{Limit: 10})
		if err != nil || len(page.Items) != 1 {
			t.Fatal(page, err)
		}
		def.GroupID = "other"
		def.Version = 2
		if _, err = q.Store.Put(ctx, "definition", def.ID, 1, def); err != nil {
			t.Fatal(err)
		}
		page, err = q.Page(ctx, p, "executions", model.QueryRequest{Limit: 10})
		if err != nil || len(page.Items) != 0 {
			t.Fatal(page, err)
		}
		def.GroupID = "factory"
		def.Version = 3
		if _, err = q.Store.Put(ctx, "definition", def.ID, 2, def); err != nil {
			t.Fatal(err)
		}
		page, err = q.Page(ctx, p, "executions", model.QueryRequest{Limit: 10})
		if err != nil || len(page.Items) != 1 {
			t.Fatal(page, err)
		}
		if err = q.Store.Write(ctx, func(tx *store.Tx) error {
			return tx.SaveControlEvidence(model.CommandEvidence{ID: "private", ExecutionID: "run", DeviceID: "device-b"})
		}); err != nil {
			t.Fatal(err)
		}
		page, err = q.Page(ctx, p, "executions", model.QueryRequest{Limit: 10})
		if err != nil || len(page.Items) != 0 {
			t.Fatal(page, err)
		}
		if _, err = q.Control.Detail(ctx, p, "run"); !errors.Is(err, identity.ErrDenied) {
			t.Fatalf("detail exposed private evidence: %v", err)
		}
	})
}
func TestQueryDrainCompetingReader(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000; i++ {
			q := &Queries{groups: map[string]*queryGroup{}}
			sub := &QuerySubscription{events: make(chan model.QueryEvent, 1), done: make(chan struct{})}
			sub.events <- model.QueryEvent{Type: "snapshot"}
			g := &queryGroup{key: "test", subs: map[*QuerySubscription]bool{sub: true}}
			q.groups[g.key] = g
			consumed := make(chan struct{})
			go func() {
				defer close(consumed)
				for range sub.events {
				}
			}()
			q.mu.Lock()
			q.sendQuery(g, sub, model.QueryEvent{Type: "delta"})
			q.removeSubscription(g, sub)
			q.mu.Unlock()
			<-consumed
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queue drain blocked against competing receiver")
	}
	t.Log("10000 full queue / competing reader interleavings completed with no stranded sender or receiver")
}
func TestQueryTrendExactValueHeartbeatAndIdentity(t *testing.T) {
	q, p := queryAppFixture(t, false)
	ctx := context.Background()
	var now atomic.Int64
	now.Store(1000000)
	q.Store.Now = func() time.Time { return time.UnixMilli(now.Load()) }
	err := q.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SetEphemeral("source", "device-a", model.SourceState{ID: "device-a", LastSeenMS: now.Load(), Status: "online"}); err != nil {
			return err
		}
		return tx.InsertPoint(model.Observation{ID: "exact", DeviceID: "device-a", Key: "counter", Value: json.Number("18446744073709551615"), ObservedMS: 999999, Quality: "GOOD"})
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := model.QueryRequest{Limit: 10, FromMS: 1, ToMS: 2000000, Resolution: "raw"}
	sub, err := q.Subscribe(ctx, p, "trend", opts, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	initial := queryEvent(t, sub, "snapshot")
	if len(initial.Page.Items) != 1 || !strings.Contains(string(initial.Page.Items[0].Data), "18446744073709551615") || initial.Page.Metadata.Quality.Good != 1 {
		t.Fatal(initial)
	}
	now.Add(q.Store.Policy().OfflineMS + 1)
	offline := queryEvent(t, sub, "delta")
	if offline.Cursor != initial.Cursor || offline.Metadata.Sources[0].Status != "offline" {
		t.Fatal(offline)
	}
	other := p
	other.SessionID = "different-direct-session"
	second, err := q.Subscribe(ctx, other, "trend", opts, "")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	queryEvent(t, second, "snapshot")
	if q.SubscriptionStats().Groups != 2 {
		t.Fatal("distinct sessions shared their authorization cache")
	}
	resumed, err := q.Subscribe(ctx, p, "trend", opts, initial.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	restored := queryEvent(t, resumed, "delta")
	if restored.Metadata.Sources[0].Status != "offline" {
		t.Fatal(restored)
	}
	sub.Close()
	second.Close()
	resumed.Close()
	awaitQuery(t, q, func(s QuerySubscriptionStats) bool { return s.ActiveLoops == 0 })
}

func TestQueryApplicationSQLite(t *testing.T)   { queryApplicationSuite(t, false) }
func TestQueryApplicationPostgres(t *testing.T) { queryApplicationSuite(t, true) }

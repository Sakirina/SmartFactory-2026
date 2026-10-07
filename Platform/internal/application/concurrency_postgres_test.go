package application

import (
	"context"
	"fmt"
	"testing"
	"time"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/internal/testdb"
	"competition2026/product/platform/pkg/model"
)

func TestPostgresIndependentDraftsShareAuthorizationWithoutSerialization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dsn, admin := testdb.Postgres(t, "application")
	database, err := store.Open(ctx, dsn, "application-pg", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	database.DB.SetMaxOpenConns(3)
	t.Cleanup(func() { database.Close() })
	svc := &Definitions{Store: database, Identity: &identity.Manager{Store: database}, Engine: &engine.Service{Store: database}, Mode: "cloud"}
	user := model.User{ID: "engineer", Version: 1, Active: true, Roles: []string{"engineer"}, Teams: []string{"team"}}
	for _, record := range []struct {
		kind, id string
		v        any
	}{{"user", user.ID, user}, {"grant", "access", identity.Grant{ID: "access", TeamID: "team", GroupID: "factory", Resources: []string{"factory"}, Actions: []string{"draft", "read"}}}, {"entity", "factory", model.Entity{ID: "factory", Kind: "asset"}}, {"entity", "device", model.Entity{ID: "device", Kind: "device", ParentID: "factory"}}} {
		if _, err = database.Put(ctx, record.kind, record.id, 0, record.v); err != nil {
			t.Fatal(err)
		}
	}
	principal := identity.Principal{User: user, Actor: model.Actor{UserID: user.ID, Source: "test"}}
	drafts := []model.Draft{}
	for _, id := range []string{"one", "two"} {
		draft := model.Draft{ID: id, Definition: model.Definition{ID: "rule-" + id, Kind: "analysis", GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"device"}}}}
		saved, err := svc.SaveDraft(ctx, principal, SaveDraftInput{Draft: draft})
		if err != nil {
			t.Fatal(err)
		}
		drafts = append(drafts, saved)
	}
	// Each application call must reach its own UPDATE while both read the same
	// user, grant collection and ancestry. Database triggers are the observation
	// point; no test hook bypasses application authorization or its transaction.
	if _, err = database.DB.ExecContext(ctx, `CREATE FUNCTION pause_draft() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='draft' THEN IF NEW.id='one' THEN PERFORM pg_advisory_xact_lock(881990001); ELSIF NEW.id='two' THEN PERFORM pg_advisory_xact_lock(881990002); END IF; END IF; RETURN NEW; END $$; CREATE TRIGGER pause_drafts BEFORE UPDATE ON documents FOR EACH ROW EXECUTE FUNCTION pause_draft()`); err != nil {
		t.Fatal(err)
	}
	blocker, err := admin.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err = blocker.ExecContext(ctx, "SELECT pg_advisory_lock(881990001),pg_advisory_lock(881990002)"); err != nil {
		t.Fatal(err)
	}
	defer blocker.ExecContext(context.Background(), "SELECT pg_advisory_unlock_all()")
	done := make(chan error, 2)
	start := time.Now()
	for _, draft := range drafts {
		go func() {
			draft.Definition.Name = "updated"
			_, err := svc.SaveDraft(ctx, principal, SaveDraftInput{Draft: draft, ExpectedVersion: draft.Version})
			done <- err
		}()
	}
	concurrent := false
	for time.Since(start) < 5*time.Second {
		var n int
		if err = blocker.QueryRowContext(ctx, "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND objid IN (881990001,881990002)").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 2 {
			concurrent = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = blocker.ExecContext(ctx, "SELECT pg_advisory_unlock_all()"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = <-done; err != nil {
			t.Fatal(err)
		}
	}
	if !concurrent {
		t.Fatal("independent SaveDraft calls serialized on shared authorization reads")
	}
	for _, id := range []string{"one", "two"} {
		doc, err := database.Get(ctx, "draft", id)
		if err != nil || doc.Version != 2 {
			t.Fatal(doc, err)
		}
	}
	if issues, err := database.VerifyAudit(ctx); err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
	t.Log(fmt.Sprintf("two authenticated SaveDraft calls simultaneously reached distinct UPDATE triggers in %s; shared user/grant/asset reads; both versions 2; audit verified", time.Since(start)))
}

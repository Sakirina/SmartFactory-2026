package compiledplan_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/compiledplan"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func TestPostgresPlanTransactionsAndConcurrentPublication(t *testing.T) {
	dsn := os.Getenv("SF_TEST_POSTGRES_DATABASE")
	if dsn == "" {
		t.Skip("isolated PostgreSQL fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("could not open PostgreSQL fixture")
	}
	admin.SetMaxOpenConns(1)
	admin.SetMaxIdleConns(1)
	defer admin.Close()
	schema := fmt.Sprintf("sf_rules_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	address, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL fixture URL")
	}
	query := address.Query()
	query.Set("search_path", schema)
	address.RawQuery = query.Encode()
	db, err := store.Open(ctx, address.String(), "rules-fixture", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.DB.SetMaxOpenConns(3)
	db.DB.SetMaxIdleConns(3)
	defer db.Close()
	d := definition("concurrent")
	plan, err := compiledplan.Compile(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Go(func() { results <- db.Write(ctx, func(tx *store.Tx) error { return compiledplan.Put(tx, d, plan) }) })
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("concurrent identical plan", err)
		}
	}
	doc, err := db.Get(ctx, "compiled_plan", plan.ID)
	if err != nil || doc.Version != 1 {
		t.Fatal("immutable plan version changed", doc.Version, err)
	}
	consumer := definition("consumer")
	consumer.Dependencies = []string{"dependency"}
	dependency := definition("dependency")
	dependency.EffectiveMS = 500
	batch := []model.Definition{consumer, dependency}
	importBatch := func(tx *store.Tx) error {
		for _, item := range batch {
			data, err := json.Marshal(item)
			if err != nil {
				return err
			}
			if _, err := tx.ImportDocument(store.Document{Kind: "definition", ID: item.ID, Version: item.Version, UpdatedMS: item.EffectiveMS, Data: data}); err != nil {
				return err
			}
		}
		for _, item := range batch {
			if err := compiledplan.Import(tx, item); err != nil {
				return err
			}
		}
		return compiledplan.RetryIssues(tx)
	}
	failure := definition("corrupt")
	failure.ExecutionPlan, err = compiledplan.Compile(ctx, failure)
	if err != nil {
		t.Fatal(err)
	}
	failure.ExecutionPlan.Format = "unknown"
	err = db.Write(ctx, func(tx *store.Tx) error {
		if err := importBatch(tx); err != nil {
			return err
		}
		if err := tx.Audit(model.Actor{UserID: "fixture"}, "metadata.import", "consumer", "batch", consumer); err != nil {
			return err
		}
		if err := tx.SetEphemeral("sync_cursor", "cloud", map[string]int{"download": 42}); err != nil {
			return err
		}
		return compiledplan.Import(tx, failure)
	})
	if err == nil {
		t.Fatal("invalid plan transaction committed")
	}
	for _, kind := range []string{"definition", "sync_cursor", "plan_issue"} {
		documents, err := db.List(ctx, kind)
		if err != nil || len(documents) != 0 {
			t.Fatal("rollback lost atomicity", kind, len(documents), err)
		}
	}
	history, err := db.Versions(ctx, "definition", "consumer")
	if err != nil || len(history) != 0 {
		t.Fatal("history survived rollback", history, err)
	}
	audit, err := db.AuditList(ctx, "batch", 100)
	if err != nil || len(audit) != 0 {
		t.Fatal("audit survived rollback", audit, err)
	}
	if err := db.Write(ctx, importBatch); err != nil {
		t.Fatal("valid batch dependency preparation", err)
	}
	if _, err := compiledplan.Load(ctx, db, consumer); err != nil {
		t.Fatal(err)
	}
	t.Logf("PostgreSQL: 8 concurrent identical puts kept plan version=1; consumer-before-dependency prepared; corrupt plan rolled back definitions/history/cursor/audit; pool max_open=%d plus one schema administration connection", db.DB.Stats().MaxOpenConnections)
}

package compiledplan_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/compiledplan"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func database(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "plans.db"), "test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.Now = func() time.Time { return time.UnixMilli(10000) }
	return db
}

func definition(id string) model.Definition {
	return model.Definition{ID: id, Name: id, Kind: "analysis", SchemaVersion: model.ContractVersion, Status: "published", Version: 1, EffectiveMS: 1000, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"value"}}, Nodes: []model.Node{{ID: "expr", Type: "expression", Params: map[string]any{"code": "value+1"}}}}
}

func putDefinition(t *testing.T, db *store.Store, definition model.Definition) {
	t.Helper()
	if _, err := db.Put(context.Background(), "definition", definition.ID, definition.Version-1, definition); err != nil {
		t.Fatal(err)
	}
}

func TestImmutablePlanSurvivesRestartAndRejectsConflictingContent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	db, err := store.Open(ctx, path, "test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	d := definition("plan")
	putDefinition(t, db, d)
	service := &engine.Service{Store: db}
	if err := service.PrepareDefinition(ctx, d); err != nil {
		t.Fatal(err)
	}
	plan, err := compiledplan.Load(ctx, db, d)
	if err != nil {
		t.Fatal(err)
	}
	before := rulecore.SnapshotStatistics()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, path, "test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	service = &engine.Service{Store: db}
	prepared, err := service.PreparePublishedPlans(ctx)
	if err != nil || prepared.Prepared != 1 || len(prepared.Isolated) != 0 {
		t.Fatal(prepared, err)
	}
	after := rulecore.SnapshotStatistics()
	restored, err := compiledplan.Load(ctx, db, d)
	if err != nil || restored.ID != plan.ID || restored.SHA256 != plan.SHA256 {
		t.Fatal("reopened database changed plan identity", restored, err)
	}
	t.Logf("restart plan_id=%s plan_sha256=%s compilations=%d expression_parses=%d", plan.ID, plan.SHA256, after.Compilations-before.Compilations, after.ExpressionParses-before.ExpressionParses)
	if after.Compilations != before.Compilations || after.ExpressionParses != before.ExpressionParses {
		t.Fatal("restart compiled persisted plan", before, after)
	}
	if err := db.Write(ctx, func(tx *store.Tx) error { return compiledplan.Put(tx, d, plan) }); err != nil {
		t.Fatal(err)
	}
	document, err := db.Get(ctx, "compiled_plan", plan.ID)
	if err != nil || document.Version != 1 {
		t.Fatal("immutable plan rewritten", document, err)
	}
	corrupt := *plan
	corrupt.SHA256 = "bad"
	if err := db.Write(ctx, func(tx *store.Tx) error { return compiledplan.Put(tx, d, &corrupt) }); err == nil {
		t.Fatal("different plan content accepted")
	}
}

func TestLegacyHistoricalIsolationAndDependencyRecovery(t *testing.T) {
	ctx := context.Background()
	db := database(t)
	old := definition("versioned")
	old.Nodes[0].Params["code"] = "missing + 1"
	putDefinition(t, db, old)
	current := definition("versioned")
	current.Version = 2
	current.EffectiveMS = 2000
	putDefinition(t, db, current)
	dependent := definition("dependent")
	dependent.Dependencies = []string{"late-dependency"}
	putDefinition(t, db, dependent)
	service := &engine.Service{Store: db}
	prepared, err := service.PreparePublishedPlans(ctx)
	if err != nil || prepared.Prepared != 1 || len(prepared.Isolated) != 2 {
		t.Fatal(prepared, err)
	}
	if _, err := compiledplan.Load(ctx, db, current); err != nil {
		t.Fatal(err)
	}
	if _, err := compiledplan.Load(ctx, db, old); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("invalid old version was prepared", err)
	}
	dependency := definition("late-dependency")
	dependency.EffectiveMS = 500
	putDefinition(t, db, dependency)
	if err := db.Write(ctx, compiledplan.RetryIssues); err != nil {
		t.Fatal(err)
	}
	if _, err := compiledplan.Load(ctx, db, dependent); err != nil {
		t.Fatal("dependency arrival did not recover legacy plan", err)
	}
	issues, err := db.List(ctx, "plan_issue")
	if err != nil || len(issues) != 1 {
		t.Fatal("recovered issue not cleared", issues, err)
	}
	diagnostics, err := service.PlanDiagnostics(ctx, "versioned")
	if err != nil || len(diagnostics) != 2 || diagnostics[0].Status != "isolated" || diagnostics[1].Status != "prepared" {
		t.Fatal(diagnostics, err)
	}
}

func TestHistoricalDependencySelectionAndCycles(t *testing.T) {
	ctx := context.Background()
	db := database(t)
	dep := definition("dependency")
	dep.EffectiveMS = 500
	putDefinition(t, db, dep)
	inactive := dep
	inactive.Version = 2
	inactive.EffectiveMS = 2000
	inactive.Status = "inactive"
	putDefinition(t, db, inactive)
	old := definition("dependent")
	old.Dependencies = []string{"dependency"}
	old.EffectiveMS = 1500
	putDefinition(t, db, old)
	current := old
	current.Version = 2
	current.EffectiveMS = 2500
	putDefinition(t, db, current)
	first := definition("cycle-a")
	first.Dependencies = []string{"cycle-b"}
	putDefinition(t, db, first)
	second := definition("cycle-b")
	second.Dependencies = []string{"cycle-a"}
	putDefinition(t, db, second)
	service := &engine.Service{Store: db}
	prepared, err := service.PreparePublishedPlans(ctx)
	if err != nil || prepared.Prepared != 2 || len(prepared.Isolated) != 3 {
		t.Fatal(prepared, err)
	}
	if _, err := compiledplan.Load(ctx, db, old); err != nil {
		t.Fatal("historical active dependency not selected", err)
	}
}

func TestStorageFailureIsNotConvertedToRuleIsolation(t *testing.T) {
	ctx := context.Background()
	db := database(t)
	d := definition("write-failure")
	putDefinition(t, db, d)
	if _, err := db.DB.Exec("CREATE TRIGGER reject_plan BEFORE INSERT ON documents WHEN NEW.kind='compiled_plan' BEGIN SELECT RAISE(ABORT, 'injected storage failure'); END"); err != nil {
		t.Fatal(err)
	}
	service := &engine.Service{Store: db}
	_, err := service.PreparePublishedPlans(ctx)
	var semantic *compiledplan.DefinitionError
	if err == nil || errors.As(err, &semantic) || !strings.Contains(err.Error(), "injected storage failure") {
		t.Fatal("storage error hidden", err)
	}
	issues, err := db.List(ctx, "plan_issue")
	if err != nil || len(issues) != 0 {
		t.Fatal("database failure created rule issue", issues, err)
	}
	if _, err := compiledplan.Load(ctx, db, d); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed write partially committed", err)
	}
}

func TestCorruptPersistedPlanAbortsStartup(t *testing.T) {
	ctx := context.Background()
	db := database(t)
	d := definition("corrupt")
	putDefinition(t, db, d)
	service := &engine.Service{Store: db}
	if err := service.PrepareDefinition(ctx, d); err != nil {
		t.Fatal(err)
	}
	plan, err := compiledplan.Load(ctx, db, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec("UPDATE documents SET data='{}' WHERE kind='compiled_plan' AND id=$1", plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PreparePublishedPlans(ctx); err == nil || !strings.Contains(err.Error(), "corrupt stored execution plan") {
		t.Fatal("corrupt stored plan ignored", err)
	}
	issues, err := db.List(ctx, "plan_issue")
	if err != nil || len(issues) != 0 {
		t.Fatal(issues, err)
	}
}

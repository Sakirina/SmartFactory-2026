package cloudsync

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"competition2026/product/platform/internal/compiledplan"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func importDefinition(id string) model.Definition {
	return model.Definition{ID: id, Name: id, Kind: "analysis", SchemaVersion: model.ContractVersion, Status: "published", Version: 1, EffectiveMS: 1000, GroupID: "factory", Nodes: []model.Node{{ID: "input", Type: "input"}}}
}

func definitionChange(t *testing.T, definition model.Definition, sequence int64) store.Change {
	t.Helper()
	data, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	return store.Change{Sequence: sequence, Document: store.Document{Kind: "definition", ID: definition.ID, Version: definition.Version, UpdatedMS: definition.EffectiveMS, Data: data}}
}

func TestImportPreparesEarlierConsumerUsingLaterDependencyAndOriginalVersions(t *testing.T) {
	ctx := context.Background()
	for _, reverse := range []bool{false, true} {
		f := setup(t)
		importer := &Server{Store: f.edge, Identity: f.client.Identity}
		consumer := importDefinition("consumer")
		consumer.Dependencies = []string{"dependency"}
		consumer.Version = 4
		dependency := importDefinition("dependency")
		dependency.Version = 9
		dependency.EffectiveMS = 500
		changes := []store.Change{definitionChange(t, consumer, 2), definitionChange(t, dependency, 1)}
		if reverse {
			changes[0], changes[1] = changes[1], changes[0]
		}
		if err := importer.importChanges(ctx, "edge-a", changes, false); err != nil {
			t.Fatal(err)
		}
		for _, definition := range []model.Definition{consumer, dependency} {
			doc, err := f.edge.Get(ctx, "definition", definition.ID)
			if err != nil || doc.Version != definition.Version {
				t.Fatal("source identity changed", doc, err)
			}
			plan, err := compiledplan.Load(ctx, f.edge, definition)
			if err != nil || plan.DefinitionVersion != definition.Version {
				t.Fatal(plan, err)
			}
			versions, err := f.edge.Versions(ctx, "definition", definition.ID)
			if err != nil || len(versions) != 1 || versions[0].UpdatedMS != definition.EffectiveMS {
				t.Fatal("source history changed", versions, err)
			}
		}
	}
}

func TestLegacyImportIsolatesInvalidDefinitionAndRecoversMissingDependency(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	importer := &Server{Store: f.edge, Identity: f.client.Identity}
	invalid := importDefinition("invalid")
	invalid.Nodes = nil
	consumer := importDefinition("consumer")
	consumer.Dependencies = []string{"dependency"}
	if err := importer.importChanges(ctx, "edge-a", []store.Change{definitionChange(t, invalid, 1), definitionChange(t, consumer, 2)}, false); err != nil {
		t.Fatal(err)
	}
	issues, err := f.edge.List(ctx, "plan_issue")
	if err != nil || len(issues) != 2 {
		t.Fatal(issues, err)
	}
	deliveries, err := f.edge.Deliveries(ctx, "tb_definition", 100)
	if err != nil || len(deliveries) != 0 {
		t.Fatal("isolated definition reached native execution", deliveries, err)
	}
	dependency := importDefinition("dependency")
	dependency.EffectiveMS = 500
	if err := importer.importChanges(ctx, "edge-a", []store.Change{definitionChange(t, dependency, 3)}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := compiledplan.Load(ctx, f.edge, consumer); err != nil {
		t.Fatal("separate batch dependency did not recover", err)
	}
	issues, err = f.edge.List(ctx, "plan_issue")
	if err != nil || len(issues) != 1 {
		t.Fatal(issues, err)
	}
}

func TestCorruptImportedPlanRollsBackEarlierDefinitionsHistoriesCursorAndAudit(t *testing.T) {
	ctx := context.Background()
	for _, failure := range []string{"format", "checksum", "content"} {
		t.Run(failure, func(t *testing.T) {
			f := setup(t)
			if err := f.client.Exchange(ctx); err != nil {
				t.Fatal(err)
			}
			cursorBefore, err := f.edge.Get(ctx, "sync_cursor", "cloud")
			if err != nil {
				t.Fatal(err)
			}
			auditBefore, err := f.edge.ExportAudit(ctx, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			valid := importDefinition("first-valid")
			if _, err := f.cloud.Put(ctx, "definition", valid.ID, 0, valid); err != nil {
				t.Fatal(err)
			}
			invalid := importDefinition("last-invalid")
			plan, err := compiledplan.Compile(ctx, invalid)
			if err != nil {
				t.Fatal(err)
			}
			invalid.ExecutionPlan = plan
			switch failure {
			case "format":
				plan.Format = "smartfactory.rules/99"
			case "checksum":
				plan.SHA256 = "incorrect"
			case "content":
				invalid.Name = "different content"
			}
			if _, err := f.cloud.Put(ctx, "definition", invalid.ID, 0, invalid); err != nil {
				t.Fatal(err)
			}
			if err := f.client.Exchange(ctx); err == nil {
				t.Fatal("invalid remote plan accepted")
			}
			for _, id := range []string{valid.ID, invalid.ID} {
				if _, err := f.edge.Get(ctx, "definition", id); !errors.Is(err, store.ErrNotFound) {
					t.Fatal("partial metadata committed", id, err)
				}
				versions, err := f.edge.Versions(ctx, "definition", id)
				if err != nil || len(versions) != 0 {
					t.Fatal("partial history committed", versions, err)
				}
			}
			plans, err := f.edge.List(ctx, "compiled_plan")
			if err != nil || len(plans) != 0 {
				t.Fatal("partial execution plan committed", plans, err)
			}
			cursorAfter, err := f.edge.Get(ctx, "sync_cursor", "cloud")
			if err != nil || !reflect.DeepEqual(cursorBefore, cursorAfter) {
				t.Fatal("cursor skipped rejected batch", cursorBefore, cursorAfter, err)
			}
			auditAfter, err := f.edge.ExportAudit(ctx, 0, 100)
			if err != nil || !reflect.DeepEqual(auditBefore, auditAfter) {
				t.Fatal("rejected batch changed audit", auditBefore, auditAfter, err)
			}
		})
	}
}

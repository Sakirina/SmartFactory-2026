package compatibility_test

import (
	"testing"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/compatibility"
)

func TestCompatibilityDatabaseRangeMatchesEmbeddedMigrations(t *testing.T) {
	manifest := compatibility.Current()
	for _, driver := range []string{"sqlite", "postgres"} {
		storeDriver := driver
		if driver == "postgres" {
			storeDriver = "pgx"
		}
		records, err := store.MigrationManifest(storeDriver)
		if err != nil || len(records) == 0 {
			t.Fatalf("%s migration manifest: %v", driver, err)
		}
		latest := records[len(records)-1].Version
		if manifest.Databases[driver].MigrationMaximum != latest {
			t.Fatalf("%s compatibility declares %d, binary migrates to %d", driver, manifest.Databases[driver].MigrationMaximum, latest)
		}
		if err := compatibility.CheckDatabase(driver, latest); err != nil {
			t.Fatal(err)
		}
	}
}

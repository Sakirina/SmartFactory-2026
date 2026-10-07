package compatibility

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"competition2026/product/platform/pkg/model"
)

func TestCurrentAndLegacyCloudEdgeCombinations(t *testing.T) {
	for _, role := range []string{"cloud", "edge"} {
		for _, peer := range []*Peer{nil, CurrentPeer(role)} {
			if err := CheckPeer(peer, "cloud-edge-v1", role); err != nil {
				t.Fatal(role, err)
			}
		}
	}
	for _, protocol := range []string{"sync", "business_contract", "definition"} {
		for _, version := range []string{"", "v2", "2.0"} {
			peer := CurrentPeer("edge")
			peer.Protocols[protocol] = version
			if err := CheckPeer(peer, "cloud-edge-v1", "edge"); err == nil {
				t.Fatalf("accepted %s=%q", protocol, version)
			}
		}
	}
	if err := CheckPeer(CurrentPeer("cloud"), "cloud-edge-v1", "edge"); err == nil {
		t.Fatal("accepted the wrong advertised role")
	}
	if err := CheckPeer(nil, "unknown", "edge"); err == nil {
		t.Fatal("accepted an undeclared combination")
	}
}

func TestManifestCopiesAndPublishedFile(t *testing.T) {
	manifest := Current()
	if manifest.ContractVersion != model.ContractVersion {
		t.Fatal("contract mismatch")
	}
	manifest.Protocols["sync"] = Protocol{Current: "corrupt"}
	if CurrentPeer("edge").Protocols["sync"] != "v1" {
		t.Fatal("mutable shared manifest")
	}
	published, err := os.ReadFile("../../../contracts/compatibility.json")
	if err != nil {
		t.Fatal(err)
	}
	var generated Manifest
	if err := json.Unmarshal(published, &generated); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(generated, Current()) {
		t.Fatal("published compatibility manifest differs; regenerate contracts")
	}
}

func TestDatabaseMigrationRange(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres", "pgx"} {
		name := driver
		if name == "pgx" {
			name = "postgres"
		}
		policy := Current().Databases[name]
		for version := policy.MigrationMinimum; version <= policy.MigrationMaximum; version++ {
			if err := CheckDatabase(driver, version); err != nil {
				t.Fatal(err)
			}
		}
		for _, version := range []int64{0, policy.MigrationMinimum - 1, policy.MigrationMaximum + 1} {
			if err := CheckDatabase(driver, version); err == nil {
				t.Fatalf("accepted %s migration %d", driver, version)
			}
		}
	}
}

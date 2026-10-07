// Package compatibility declares wire formats and checks them before data is applied.
package compatibility

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"competition2026/product/platform/pkg/model"
)

//go:embed manifest.json
var source []byte

type Protocol struct {
	Current string   `json:"current"`
	Read    []string `json:"read"`
	Write   []string `json:"write"`
}

type Database struct {
	MigrationMinimum int64 `json:"migration_minimum"`
	MigrationMaximum int64 `json:"migration_maximum"`
}

type Combination struct {
	Name           string   `json:"name"`
	Roles          []string `json:"roles"`
	Protocols      []string `json:"protocols"`
	LegacyOmission bool     `json:"legacy_omission"`
}

type Manifest struct {
	SchemaVersion      int                 `json:"schema_version"`
	ApplicationVersion string              `json:"application_version"`
	ContractVersion    string              `json:"contract_version"`
	Protocols          map[string]Protocol `json:"protocols"`
	Databases          map[string]Database `json:"databases"`
	Combinations       []Combination       `json:"combinations"`
}

// Peer advertises formats only. Authentication establishes the peer identity.
type Peer struct {
	ApplicationVersion string            `json:"application_version"`
	Role               string            `json:"role"`
	Protocols          map[string]string `json:"protocols"`
}

// Current returns an independent copy so callers cannot alter later checks.
func Current() Manifest {
	var manifest Manifest
	if err := json.Unmarshal(source, &manifest); err != nil {
		panic("invalid embedded compatibility manifest: " + err.Error())
	}
	if manifest.SchemaVersion != 1 || manifest.ContractVersion != model.ContractVersion {
		panic("embedded compatibility manifest differs from the business contract")
	}
	return manifest
}

func JSON() []byte { return slices.Clone(source) }

func CurrentPeer(role string) *Peer {
	manifest := Current()
	peer := &Peer{ApplicationVersion: manifest.ApplicationVersion, Role: role, Protocols: map[string]string{}}
	for name, protocol := range manifest.Protocols {
		peer.Protocols[name] = protocol.Current
	}
	return peer
}

// CheckPeer accepts omission only for a declared legacy combination. An explicit
// advertisement must include every format needed by that operation. Each side
// sends its current format and verifies the other side's read compatibility.
func CheckPeer(peer *Peer, combination, role string) error {
	manifest := Current()
	var selected *Combination
	for i := range manifest.Combinations {
		if manifest.Combinations[i].Name == combination {
			selected = &manifest.Combinations[i]
			break
		}
	}
	if selected == nil || !slices.Contains(selected.Roles, role) {
		return fmt.Errorf("unsupported compatibility combination %q for %q", combination, role)
	}
	if peer == nil {
		if selected.LegacyOmission {
			return nil
		}
		return fmt.Errorf("%s requires a version advertisement", combination)
	}
	if peer.Role != role {
		return fmt.Errorf("compatibility role %q does not match %q", peer.Role, role)
	}
	for _, name := range selected.Protocols {
		format, ok := manifest.Protocols[name]
		if !ok {
			return fmt.Errorf("compatibility combination contains unknown protocol %q", name)
		}
		if version := peer.Protocols[name]; !slices.Contains(format.Read, version) {
			return fmt.Errorf("unsupported %s version %q; readable versions are %v", name, version, format.Read)
		}
	}
	return nil
}

func CheckDatabase(driver string, version int64) error {
	if driver == "pgx" {
		driver = "postgres"
	}
	policy, ok := Current().Databases[driver]
	if !ok || version < policy.MigrationMinimum || version > policy.MigrationMaximum {
		return fmt.Errorf("unsupported %s migration version %d", driver, version)
	}
	return nil
}

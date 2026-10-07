package releasebundle_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/pkg/model"
)

func validManifest(t *testing.T) model.ReleaseManifest {
	t.Helper()
	d := model.Definition{ID: "count", Name: "Count", SchemaVersion: model.ContractVersion, Kind: "analysis", Status: "published", Version: 1, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"sensor"}, Keys: []string{"pulse"}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "count", Type: "counter", Params: map[string]any{"mode": "delta"}}, {ID: "output", Type: "output"}}, Connections: []model.Connection{{From: "input", To: "count"}, {From: "count", To: "output"}}, Outputs: []model.Output{{Key: "total", Type: "integer", NodeID: "output"}}}
	raw, _ := json.Marshal(d)
	ruleSHA, _ := releasebundle.ContentDigest(raw)
	build := model.ProgramBuild{Program: "edge", Version: "1", GOOS: "linux", GOARCH: "amd64", MigrationMinimum: 9, MigrationMaximum: 11, RuleFormats: []string{model.ContractVersion}, ConfigurationFormats: []string{releasebundle.ConfigurationFormat}}
	ref := model.ConfigurationReference{Kind: "parameter", ID: "heartbeat.interval_ms", Version: 1, Digest: strings.Repeat("b", 64)}
	return model.ReleaseManifest{SchemaVersion: releasebundle.Format, ID: "release", Name: "Release", Program: "edge", Components: []model.ReleaseComponent{{ID: "program", Kind: "program", Version: "1", SHA256: strings.Repeat("a", 64), Format: releasebundle.ProgramFormat, Build: &build}, {ID: "parameter", Kind: "configuration", Version: "1", SHA256: ref.Digest, Format: releasebundle.ConfigurationFormat, Configuration: &ref}, {ID: "count", Kind: "rule", Version: "1", SHA256: ruleSHA, Format: model.ContractVersion, Content: raw, DependsOn: []model.ReleaseDependency{{ID: "parameter", Version: "1", SHA256: ref.Digest}}}}}
}

func TestReleaseBundleDependencyContentCompatibilityAndNodeScope(t *testing.T) {
	ctx := context.Background()
	m := validManifest(t)
	if v := releasebundle.Validate(ctx, m); !v.Valid {
		t.Fatal(v.Issues)
	}
	for _, tc := range []struct {
		name, code string
		edit       func(*model.ReleaseManifest)
	}{
		{"rule content changed", "digest", func(m *model.ReleaseManifest) { m.Components[2].Content = json.RawMessage(`{"different":true}`) }},
		{"dependency absent", "dependency", func(m *model.ReleaseManifest) { m.Components[2].DependsOn[0].ID = "absent" }},
		{"dependency version changed", "dependency", func(m *model.ReleaseManifest) { m.Components[2].DependsOn[0].Version = "2" }},
		{"cycle", "cycle", func(m *model.ReleaseManifest) {
			r := m.Components[2]
			m.Components[1].DependsOn = []model.ReleaseDependency{{ID: r.ID, Version: r.Version, SHA256: r.SHA256}}
		}},
		{"program cannot read rule", "program_rule_compatibility", func(m *model.ReleaseManifest) { m.Components[0].Build.RuleFormats = []string{"2.0"} }},
		{"configuration digest changed", "configuration_format", func(m *model.ReleaseManifest) { m.Components[1].Configuration.Digest = strings.Repeat("c", 64) }},
		{"cross node dependency", "dependency_target", func(m *model.ReleaseManifest) {
			m.Components[1].TargetNodeIDs = []string{"edge-a"}
			m.Components[2].TargetNodeIDs = []string{"edge-b"}
		}},
		{"missing configuration", "components", func(m *model.ReleaseManifest) { m.Components = append(m.Components[:1], m.Components[2:]...) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest(t)
			tc.edit(&m)
			v := releasebundle.Validate(ctx, m)
			if v.Valid {
				t.Fatal("invalid release accepted")
			}
			found := false
			for _, issue := range v.Issues {
				found = found || issue.Code == tc.code
			}
			if !found {
				t.Fatal(tc.code, v.Issues)
			}
		})
	}
	m = validManifest(t)
	m.Components[1].TargetNodeIDs = []string{"edge-a"}
	m.Components[2].TargetNodeIDs = []string{"edge-a"}
	if len(releasebundle.ReferencesForNode(m, "edge-a")) != 1 || len(releasebundle.ReferencesForNode(m, "edge-b")) != 0 || len(releasebundle.ComponentsForNode(m, "edge-b")) != 1 {
		t.Fatal("node applicability lost")
	}
	if err := releasebundle.CheckDatabase(*m.Components[0].Build, 12); err == nil || !strings.Contains(err.Error(), "restore the verified backup") || !strings.Contains(err.Error(), "separate directory") {
		t.Fatal(err)
	}
}

func TestReleaseArtifactTamperIsExplicit(t *testing.T) {
	root := t.TempDir()
	body := "a bounded executable fixture"
	digest := releasebundle.Digest([]byte(body))
	if _, e := releasebundle.WriteArtifact(root, digest, strings.NewReader(body)); e != nil {
		t.Fatal(e)
	}
	path, _ := releasebundle.ArtifactPath(root, digest)
	if e := os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := releasebundle.VerifyArtifact(root, digest); !errors.Is(e, releasebundle.ErrArtifactIntegrity) {
		t.Fatal(e)
	}
	if _, e := releasebundle.ArtifactPath(root, "../outside"); e == nil {
		t.Fatal("invalid artifact path accepted")
	}
}

// Package releasebundle validates immutable release content and its dependency
// graph. The same validator runs in the coordinator and on the target node.
package releasebundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

const Format = "smartfactory-release-v1"
const ProgramFormat = "smartfactory-executable-v1"
const ConfigurationFormat = "smartfactory-configuration-v1"

func Digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func ValidDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
func Canonical(raw []byte) ([]byte, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("content requires one JSON value")
	}
	return json.Marshal(v)
}
func ContentDigest(raw []byte) (string, error) {
	b, e := Canonical(raw)
	if e != nil {
		return "", e
	}
	return Digest(b), nil
}
func ManifestDigest(m model.ReleaseManifest) string { return store.Hash(m) }

func Validate(ctx context.Context, m model.ReleaseManifest) model.ReleaseValidation {
	v := model.ReleaseValidation{Valid: true, SHA256: ManifestDigest(m), Order: []string{}, Issues: []model.ReleaseIssue{}}
	issue := func(id, code, msg string) {
		v.Valid = false
		v.Issues = append(v.Issues, model.ReleaseIssue{ComponentID: id, Code: code, Message: msg, Recovery: []string{}})
	}
	if m.SchemaVersion != Format || strings.TrimSpace(m.ID) == "" || len(m.ID) > 200 || strings.TrimSpace(m.Name) == "" || len(m.Name) > 200 || (m.Program != "edge" && m.Program != "cloud") {
		issue("", "manifest", "release identity, name, program or manifest format is invalid")
	}
	if len(m.Components) < 3 || len(m.Components) > 128 {
		issue("", "budget", "release requires 3 to 128 components")
	}
	byID := map[string]model.ReleaseComponent{}
	rules := map[string]model.Definition{}
	kinds := map[string]int{}
	var build *model.ProgramBuild
	for _, c := range m.Components {
		if strings.TrimSpace(c.ID) == "" || len(c.ID) > 200 || strings.TrimSpace(c.Version) == "" || !ValidDigest(c.SHA256) {
			issue(c.ID, "identity", "component requires an identity, version and lowercase SHA256")
		}
		if _, ok := byID[c.ID]; ok {
			issue(c.ID, "duplicate", "component identity is repeated")
		}
		byID[c.ID] = c
		nodes := map[string]bool{}
		for _, node := range c.TargetNodeIDs {
			if strings.TrimSpace(node) == "" || nodes[node] {
				issue(c.ID, "target", "component target nodes must be distinct nonempty identities")
			}
			nodes[node] = true
		}
		kinds[c.Kind]++
		switch c.Kind {
		case "program":
			if len(c.TargetNodeIDs) > 0 {
				issue(c.ID, "target", "the program artifact applies to every release target")
			}
			build = c.Build
			if c.Format != ProgramFormat || build == nil || len(c.Content) > 0 || c.Configuration != nil {
				issue(c.ID, "program_format", "program requires executable build metadata and an artifact digest")
				continue
			}
			if build.Program != m.Program || build.Version != c.Version || build.GOOS == "" || build.GOARCH == "" || build.MigrationMinimum < 1 || build.MigrationMaximum < build.MigrationMinimum {
				issue(c.ID, "program_build", "program version, platform or database range is invalid")
			}
		case "rule":
			if c.Build != nil || c.Configuration != nil || c.Format != model.ContractVersion {
				issue(c.ID, "rule_format", "unsupported rule format")
				continue
			}
			h, e := ContentDigest(c.Content)
			if e != nil || h != c.SHA256 {
				issue(c.ID, "digest", "rule content digest does not match")
				continue
			}
			var d model.Definition
			if e = store.DecodeJSON(c.Content, &d); e != nil || d.ID != c.ID || strconv.FormatInt(d.Version, 10) != c.Version || d.SchemaVersion != c.Format {
				issue(c.ID, "rule_identity", "rule content identity, version or format differs from the component")
				continue
			}
			_, validation := rulecore.Compile(ctx, d)
			if !validation.Valid {
				issue(c.ID, "rule_invalid", strings.Join(validation.Errors, "; "))
			}
			rules[c.ID] = d
		case "configuration":
			r := c.Configuration
			if c.Build != nil || len(c.Content) > 0 || c.Format != ConfigurationFormat || r == nil || (r.Kind != "parameter" && r.Kind != "connector") || r.ID == "" || r.Version < 1 || r.Digest != c.SHA256 || strconv.FormatInt(r.Version, 10) != c.Version {
				issue(c.ID, "configuration_format", "configuration requires an exact version and digest reference")
			}
		default:
			issue(c.ID, "kind", "unknown release component kind")
		}
	}
	if kinds["program"] != 1 || kinds["rule"] < 1 || kinds["configuration"] < 1 {
		issue("", "components", "release requires exactly one program and at least one rule and configuration")
	}
	for _, c := range m.Components {
		deps := map[string]bool{}
		for _, d := range c.DependsOn {
			other, ok := byID[d.ID]
			if !ok || other.Version != d.Version || other.SHA256 != d.SHA256 {
				issue(c.ID, "dependency", "dependency identity, version or digest is unavailable: "+d.ID)
			}
			if ok && len(other.TargetNodeIDs) > 0 {
				if len(c.TargetNodeIDs) == 0 {
					issue(c.ID, "dependency_target", "an all-node component depends on a node-specific component: "+d.ID)
				}
				for _, node := range c.TargetNodeIDs {
					if !slices.Contains(other.TargetNodeIDs, node) {
						issue(c.ID, "dependency_target", "dependency does not apply to the same node: "+d.ID)
					}
				}
			}
			if deps[d.ID] {
				issue(c.ID, "dependency", "dependency is repeated: "+d.ID)
			}
			deps[d.ID] = true
		}
		if c.Kind == "rule" {
			for _, id := range rules[c.ID].Dependencies {
				if _, ok := rules[id]; !ok || !deps[id] {
					issue(c.ID, "rule_dependency", "rule dependency must be included and pinned: "+id)
				}
			}
		}
		if build != nil {
			if c.Kind == "rule" && !slices.Contains(build.RuleFormats, c.Format) {
				issue(c.ID, "program_rule_compatibility", "program does not support this rule format")
			}
			if c.Kind == "configuration" && !slices.Contains(build.ConfigurationFormats, c.Format) {
				issue(c.ID, "program_configuration_compatibility", "program does not support this configuration format")
			}
		}
	}
	visited := map[string]int{}
	var walk func(string)
	walk = func(id string) {
		if visited[id] == 2 {
			return
		}
		if visited[id] == 1 {
			issue(id, "cycle", "release dependency graph contains a cycle")
			return
		}
		visited[id] = 1
		for _, d := range byID[id].DependsOn {
			if _, ok := byID[d.ID]; ok {
				walk(d.ID)
			}
		}
		visited[id] = 2
		v.Order = append(v.Order, id)
	}
	for _, c := range m.Components {
		walk(c.ID)
	}
	return v
}

func Program(m model.ReleaseManifest) model.ReleaseComponent {
	for _, c := range m.Components {
		if c.Kind == "program" {
			return c
		}
	}
	return model.ReleaseComponent{}
}
func References(m model.ReleaseManifest) []model.ConfigurationReference {
	out := []model.ConfigurationReference{}
	for _, c := range m.Components {
		if c.Configuration != nil {
			out = append(out, *c.Configuration)
		}
	}
	return out
}
func ComponentsForNode(m model.ReleaseManifest, nodeID string) []model.ReleaseComponent {
	out := []model.ReleaseComponent{}
	for _, c := range m.Components {
		if len(c.TargetNodeIDs) == 0 || slices.Contains(c.TargetNodeIDs, nodeID) {
			out = append(out, c)
		}
	}
	return out
}
func ReferencesForNode(m model.ReleaseManifest, nodeID string) []model.ConfigurationReference {
	m.Components = ComponentsForNode(m, nodeID)
	return References(m)
}
func CheckDatabase(build model.ProgramBuild, migration int64) error {
	if migration < build.MigrationMinimum || migration > build.MigrationMaximum {
		return fmt.Errorf("database migration %d is outside program %s supported range %d..%d; stop the workload, retain the current database, restore the verified backup paired with the selected program into a separate directory, replay data after that backup, then start and verify the restored workload", migration, build.Version, build.MigrationMinimum, build.MigrationMaximum)
	}
	return nil
}

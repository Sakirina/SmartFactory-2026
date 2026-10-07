package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/internal/releaseruntime"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/buildinfo"
	"competition2026/product/platform/pkg/model"
)

func TestReleaseStartupPreservesAuthorityVersionsAfterRemoval(t *testing.T) {
	for _, restoreHeartbeat := range []bool{false, true} {
		t.Run(strconv.FormatBool(restoreHeartbeat), func(t *testing.T) {
			ctx := context.Background()
			o, fixed := removedReleaseParameter(t)
			parameter := configcenter.Parameter{ID: "release.restore.setting", Program: "edge", Version: 7, Dynamic: true, Schema: map[string]any{"type": "integer"}, Value: 12}
			if restoreHeartbeat {
				parameter.ID, parameter.Version, parameter.Value = "heartbeat.interval_ms", 2, 2000
			}
			stageTestRelease(t, o, parameter)
			for start := 0; start < 2; start++ {
				a, err := Open(ctx, o)
				if err != nil {
					t.Fatalf("release startup %d: %v", start, err)
				}
				current, err := a.Store.Get(ctx, "parameter_version", "heartbeat.interval_ms:1")
				if err != nil || store.Hash(current) != store.Hash(fixed) {
					t.Fatalf("authority version changed: %v", err)
				}
				if _, err = a.Store.Get(ctx, "parameter_version", parameter.ID+":"+strconv.FormatInt(parameter.Version, 10)); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("release runtime created an authority version: %v", err)
				}
				want := store.DefaultPolicy().HeartbeatMS
				if restoreHeartbeat {
					want = 2000
					if err = a.Server.Config.Acknowledge(ctx, o.NodeID, parameter.ID, parameter.Version, true, ""); err != nil {
						t.Fatalf("acknowledge restored fixed version: %v", err)
					}
				} else if _, err = a.Store.Get(ctx, "parameter", "heartbeat.interval_ms"); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("removed release parameter recreated: %v", err)
				}
				if got := a.Store.Policy().HeartbeatMS; got != want {
					t.Fatalf("actual heartbeat policy = %d, want %d", got, want)
				}
				snapshot, err := a.Server.ReleaseRuntime.Snapshot(ctx)
				if err != nil || !snapshot.Healthy {
					t.Fatalf("actual release runtime unavailable: %+v %v", snapshot, err)
				}
				if problems, err := a.Store.VerifyAudit(ctx); err != nil || len(problems) != 0 {
					t.Fatalf("audit after release restore: %+v %v", problems, err)
				}
				if err = a.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestInvalidReleaseStartupPreservesRemovedParameter(t *testing.T) {
	ctx := context.Background()
	o, fixed := removedReleaseParameter(t)
	if err := os.WriteFile(o.ReleasePayload, []byte(`{"format":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if a, err := Open(ctx, o); err == nil {
		a.Close()
		t.Fatal("invalid release cache accepted")
	}
	key, err := identity.LoadMasterKey(o.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, o.DSN, o.NodeID, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Get(ctx, "parameter", "heartbeat.interval_ms"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invalid cache caused default recreation: %v", err)
	}
	current, err := s.Get(ctx, "parameter_version", "heartbeat.interval_ms:1")
	if err != nil || store.Hash(current) != store.Hash(fixed) {
		t.Fatalf("fixed authority version changed: %v", err)
	}
}

func TestReleaseRoundtripReactivatesDisjointRulesAndKeepsRestartIntegrity(t *testing.T) {
	checkReleaseRoundtrip(t, "")
}

func TestReleaseRoundtripPostgres(t *testing.T) {
	dsn := os.Getenv("SF_TEST_POSTGRES_DATABASE")
	if dsn == "" {
		t.Skip("set SF_TEST_POSTGRES_DATABASE to an isolated PostgreSQL fixture")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "sf_release_roundtrip_" + identity.ID()
	if _, err = db.ExecContext(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	checkReleaseRoundtrip(t, u.String())
}

func checkReleaseRoundtrip(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	first, fixed := removedReleaseParameter(t, dsn)
	second := first
	second.ReleasePayload += ".second"
	stageTestRelease(t, first, configcenter.Parameter{ID: "heartbeat.interval_ms", Program: "edge", Version: 2, Dynamic: true, Schema: map[string]any{"type": "integer"}, Value: 2000}, "first")
	stageTestRelease(t, second, configcenter.Parameter{ID: "release.other.setting", Program: "edge", Version: 7, Dynamic: true, Schema: map[string]any{"type": "integer"}, Value: 12}, "second")
	firstID, secondID := "release-restore-rule-first", "release-restore-rule-second"
	var restoredVersion int64
	for index, options := range []Options{first, second, first, first} {
		a, err := Open(ctx, options)
		if err != nil {
			t.Fatalf("release transition %d: %v", index, err)
		}
		if _, err := a.Server.ReleaseRuntime.Snapshot(ctx); err != nil {
			a.Close()
			t.Fatal(err)
		}
		original, err := a.Store.Get(ctx, "parameter_version", "heartbeat.interval_ms:1")
		if err != nil || store.Hash(original) != store.Hash(fixed) {
			t.Fatal("authority version changed", err)
		}
		doc, err := a.Store.Get(ctx, "definition", firstID)
		if err != nil {
			t.Fatal(err)
		}
		definition, err := store.Decode[model.Definition](doc)
		if err != nil {
			t.Fatal(err)
		}
		switch index {
		case 0:
			restoredVersion = definition.Version
		case 1:
			if definition.Status != "disabled" || definition.Version <= restoredVersion {
				t.Fatal("previous rule was not disabled with its own local revision", definition)
			}
			restoredVersion = definition.Version
		case 2:
			if definition.Status != "published" || definition.Version <= restoredVersion {
				t.Fatal("returning release did not create a new applied revision", definition)
			}
			restoredVersion = definition.Version
			other, err := a.Store.Get(ctx, "definition", secondID)
			if err != nil {
				t.Fatal(err)
			}
			otherDefinition, err := store.Decode[model.Definition](other)
			if err != nil || otherDefinition.Status != "disabled" {
				t.Fatal("outgoing release rule remained published", err)
			}
		case 3:
			if definition.Version != restoredVersion {
				t.Fatal("same release restart changed its applied revision")
			}
			definition.Name = "changed outside release activation"
			definition.Version = doc.Version + 1
			if _, err = a.Store.Put(ctx, "definition", firstID, doc.Version, definition); err != nil {
				t.Fatal(err)
			}
		}
		if issues, err := a.Store.VerifyAudit(ctx); err != nil || len(issues) != 0 {
			t.Fatal("release transition audit differs", issues, err)
		}
		if err = a.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if a, err := Open(ctx, first); err == nil {
		a.Close()
		t.Fatal("current release rule mutation was accepted")
	} else if !strings.Contains(err.Error(), "active rule differs from its release activation") && !strings.Contains(err.Error(), "execution plan does not match the definition content or version") {
		t.Fatal(err)
	}
}

func removedReleaseParameter(t *testing.T, database ...string) (Options, store.Document) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	o := Options{Mode: "edge", NodeID: "edge-a", DSN: filepath.Join(dir, "node.db"), KeyFile: filepath.Join(dir, "node.key"), BootstrapPassword: "release-restore-test-password"}
	if len(database) > 0 && database[0] != "" {
		o.DSN = database[0]
	}
	a, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := a.Store.Get(ctx, "parameter", "heartbeat.interval_ms")
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Decode[configcenter.Parameter](doc)
	if err != nil {
		t.Fatal(err)
	}
	ref := model.ConfigurationReference{Kind: "parameter", ID: p.ID, Version: p.Version, Digest: p.ContentDigest}
	envelopes := []model.ConfigurationEnvelope{{Reference: ref}}
	if err = a.Server.Config.PinLocalRelease(ctx, envelopes); err != nil {
		t.Fatal(err)
	}
	if err = a.Store.Write(ctx, func(tx *store.Tx) error {
		return tx.SetEphemeral("configuration_runtime", "parameter:"+p.ID, envelopes[0])
	}); err != nil {
		t.Fatal(err)
	}
	if err = a.Server.Config.PinLocalRelease(ctx, nil); err != nil {
		t.Fatal(err)
	}
	fixed, err := a.Store.Get(ctx, "parameter_version", "heartbeat.interval_ms:1")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	o.ReleasePayload = filepath.Join(dir, "release.json")
	return o, fixed
}

func stageTestRelease(t *testing.T, o Options, p configcenter.Parameter, variant ...string) {
	t.Helper()
	key, err := identity.LoadMasterKey(o.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := buildinfo.ExecutableSHA256()
	if err != nil {
		t.Fatal(err)
	}
	build := buildinfo.Current("edge")
	d := model.Definition{ID: "release-restore-rule", Name: "Release restore rule", SchemaVersion: model.ContractVersion, Kind: "analysis", Status: "published", Version: 1, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"restore-device"}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "sum", Type: "aggregate", Params: map[string]any{"function": "avg"}}, {ID: "output", Type: "output"}}, Connections: []model.Connection{{From: "input", To: "sum"}, {From: "sum", To: "output"}}, Outputs: []model.Output{{Key: "temperature", Type: "number", NodeID: "output"}}}
	if len(variant) > 0 {
		d.ID += "-" + variant[0]
	}
	raw, _ := json.Marshal(d)
	ruleSHA, _ := releasebundle.ContentDigest(raw)
	ref := model.ConfigurationReference{Kind: "parameter", ID: p.ID, Version: p.Version, Digest: configcenter.ParameterDigest(p)}
	manifest := model.ReleaseManifest{SchemaVersion: releasebundle.Format, ID: "release-restore", Name: "Release restore", Program: "edge", Components: []model.ReleaseComponent{{ID: "program", Kind: "program", Version: build.Version, SHA256: sha, Format: releasebundle.ProgramFormat, Build: &build}, {ID: "configuration", Kind: "configuration", Version: strconv.FormatInt(p.Version, 10), SHA256: ref.Digest, Format: releasebundle.ConfigurationFormat, Configuration: &ref}, {ID: d.ID, Kind: "rule", Version: "1", SHA256: ruleSHA, Format: model.ContractVersion, Content: raw}}}
	if len(variant) > 0 {
		manifest.ID += "-" + variant[0]
	}
	validation := releasebundle.Validate(context.Background(), manifest)
	if !validation.Valid {
		t.Fatal(validation.Issues)
	}
	staged := releaseruntime.Staged{NodeID: o.NodeID, Release: model.Release{ID: manifest.ID, SHA256: validation.SHA256, Manifest: manifest, Order: validation.Order}, Configurations: []model.ConfigurationEnvelope{{Reference: ref, NodeID: o.NodeID, Program: "edge", Purpose: "release-runtime", Dynamic: true, Schema: p.Schema, Value: p.Value}}}
	cached, err := releaseruntime.Seal(staged, &identity.Manager{Master: key})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(cached)
	if err = os.WriteFile(o.ReleasePayload, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

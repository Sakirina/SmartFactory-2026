package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type recoveryDispatcher struct{ calls int }

func (d *recoveryDispatcher) Send(context.Context, model.Step, string, int64) (control.DispatchResult, error) {
	d.calls++
	return control.DispatchResult{Status: "SUCCESS"}, nil
}

func TestStateBackupRestoresVersionsCredentialsAuditAndControlDedup(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the deployment snapshot tool")
	}
	ctx := context.Background()
	directory := t.TempDir()
	opts := Options{Mode: "edge", NodeID: "edge-a", DSN: filepath.Join(directory, "platform.db"), KeyFile: filepath.Join(directory, "master.key"), BootstrapPassword: "snapshot-test-password", ServiceToken: "snapshot-test-service", Seed: true}
	a, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	dispatch := &recoveryDispatcher{}
	a.Server.Control.Dispatcher = dispatch
	point := model.Observation{ID: "before-backup:0", DeviceID: "climate-1", Key: "interlock", Value: false, Quality: "GOOD", ObservedMS: time.Now().UnixMilli()}
	batch := store.IngestBatch{MessageID: "before-backup", SourceID: "edge-a", Points: []model.Observation{point}}
	if _, err = a.Store.Ingest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	principal := func(id string) identity.Principal {
		doc, err := a.Store.Get(ctx, "user", id)
		if err != nil {
			t.Fatal(err)
		}
		u, err := store.Decode[model.User](doc)
		if err != nil {
			t.Fatal(err)
		}
		return identity.Principal{User: u, Local: true, Actor: model.Actor{UserID: u.ID, Name: u.Name, DepartmentID: u.DepartmentID, Roles: u.Roles}}
	}
	engineer, leader := principal("engineer"), principal("leader")
	run, err := a.Server.Control.Create(ctx, engineer, "ventilation-plan", map[string]string{}, false, "restored-control")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Server.Control.Approve(ctx, engineer, run.DownlinkID, "engineer"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Server.Control.Approve(ctx, leader, run.DownlinkID, "leader"); err != nil {
		t.Fatal(err)
	}
	if run, err = a.Server.Control.Dispatch(ctx, engineer, run.DownlinkID); err != nil {
		t.Fatal(err)
	}
	if run, err = a.Server.Control.Run(ctx, run, false); err != nil || run.Status != "completed" || dispatch.calls != 1 {
		t.Fatal(err, run.Status, dispatch.calls)
	}
	if _, err = a.Server.Control.Create(ctx, engineer, "ventilation-plan", nil, false, "unfinished-control"); err != nil {
		t.Fatal(err)
	}
	parameter := configcenter.Parameter{ID: "backup.secret", Program: "edge", Category: "fixture", Schema: map[string]any{"type": "string"}, Value: "fixture-encrypted-credential", Secret: true, Dynamic: true}
	if _, err = a.Server.Config.Put(ctx, engineer.Actor, parameter, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.Put(ctx, "job", "unfinished-job", 0, model.Job{ID: "unfinished-job", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	a.Close()
	snapshot := filepath.Join(directory, "snapshot")
	restored := filepath.Join(directory, "restored")
	script := filepath.Join("..", "..", "..", "scripts", "backup-state.py")
	command := func(args ...string) {
		t.Helper()
		out, err := exec.Command("python3", append([]string{script}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("snapshot tool: %v %s", err, out)
		}
	}
	command("create", "--quiesced", "--output", snapshot, "--sqlite", "platform.db="+opts.DSN, "--file", "master.key="+opts.KeyFile)
	command("restore", snapshot, "--output", restored)
	manifest, err := os.ReadFile(filepath.Join(snapshot, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	opts.DSN, opts.KeyFile, opts.Seed = filepath.Join(restored, "platform.db"), filepath.Join(restored, "master.key"), false
	b, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if problems, err := b.Store.VerifyAudit(ctx); err != nil || len(problems) != 0 {
		t.Fatal(err, problems)
	}
	if value, err := b.Server.Config.Value(ctx, "backup.secret"); err != nil || value != parameter.Value {
		t.Fatal("encrypted credential did not restore", err)
	}
	if _, err = b.Server.Config.Put(ctx, engineer.Actor, parameter, 0); err == nil {
		t.Fatal("stale configuration version accepted after restore")
	}
	if result, err := b.Store.Ingest(ctx, batch); err != nil || !result.Duplicate {
		t.Fatal("message deduplication did not restore", result, err)
	}
	after := &recoveryDispatcher{}
	b.Server.Control.Dispatcher = after
	if result, err := b.Server.Control.Run(ctx, run, false); err != nil || result.Status != "completed" || after.calls != 0 {
		t.Fatal("restored execution repeated device action", result.Status, after.calls, err)
	}
	for kind, id := range map[string]string{"execution": "unfinished-control", "job": "unfinished-job"} {
		if _, err = b.Store.Get(ctx, kind, id); err != nil {
			t.Fatal("unfinished task missing", kind, err)
		}
	}
	var info any
	if err = json.Unmarshal(manifest, &info); err != nil {
		t.Fatal(err)
	}
	report, _ := json.MarshalIndent(map[string]any{"case": "A18", "scope": "SQLite business state and master key, actual snapshot tool and reopened application", "status": "passed", "restored_device_actions": after.calls, "audit_issues": 0, "snapshot": info}, "", "  ")
	t.Log(string(report))
	if path := os.Getenv("SF_RECOVERY_EVIDENCE"); path != "" {
		if err = os.WriteFile(path, append(report, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

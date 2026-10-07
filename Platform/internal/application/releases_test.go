package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/businessfixture"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/buildinfo"
	"competition2026/product/platform/pkg/model"
)

type releaseFixture struct {
	service     *application.Releases
	business    *businessfixture.Fixture
	manifest    model.ReleaseManifest
	release     model.Release
	nodes       map[string]nodeidentity.Principal
	credentials map[string]string
}

func newReleaseFixture(t *testing.T, pg bool) *releaseFixture {
	t.Helper()
	ctx := context.Background()
	f := businessFixture(t, pg)
	if _, e := f.Store.Put(ctx, "entity", "edge-b", 0, model.Entity{ID: "edge-b", Name: "Other edge", Kind: "edge", ParentID: "factory", Status: "active", Version: 1}); e != nil {
		t.Fatal(e)
	}
	cfg := &configcenter.Service{Store: f.Store, Identity: f.Identity}
	p, e := cfg.Put(ctx, f.Principal.Actor, configcenter.Parameter{ID: "heartbeat.interval_ms", Program: "edge", Category: "reliability", Schema: map[string]any{"type": "integer", "minimum": 1000}, Value: 2000, Dynamic: true, TargetNodeIDs: []string{"edge-a", "edge-b"}}, 0)
	if e != nil {
		t.Fatal(e)
	}
	nodes := &nodeidentity.Service{Store: f.Store, Cipher: f.Identity}
	svc := &application.Releases{Store: f.Store, Business: f.Business, Nodes: nodes, Config: cfg, ConfigurationMetadata: cfg.ConfigurationMetadata, ArtifactRoot: t.TempDir(), Mode: "cloud", OfflineAfterMS: 1000}
	out := &releaseFixture{service: svc, business: f, nodes: map[string]nodeidentity.Principal{}, credentials: map[string]string{}}
	for _, node := range []string{"edge-a", "edge-b"} {
		id := "workload-" + node
		token := strings.Repeat(node+"-credential", 4)
		out.credentials[id] = token
		e = f.Store.Write(ctx, func(tx *store.Tx) error {
			w, e := nodes.PutTx(tx, f.Principal.Actor, model.WorkloadIdentity{ID: id, NodeID: node, Program: "edge", Purpose: "release-runtime", Enabled: true, Capabilities: []string{"release", "goos:darwin", "goarch:arm64", "config.v2", "config.read", "config.report", "credential.resolve"}, ParameterIDs: []string{p.ID}}, 0)
			if e != nil {
				return e
			}
			_, e = nodes.RotateTx(tx, f.Principal.Actor, id, token, w.Version)
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
		principal, e := nodes.ActivateInstance(ctx, token, "instance-"+node)
		if e != nil {
			t.Fatal(e)
		}
		out.nodes[id] = principal
	}
	body := "unit fixture artifact, production execution is tested separately"
	sha := releasebundle.Digest([]byte(body))
	if _, e = releasebundle.WriteArtifact(svc.ArtifactRoot, sha, strings.NewReader(body)); e != nil {
		t.Fatal(e)
	}
	build := buildinfo.Current("edge")
	build.Version = "unit-v1"
	build.GOOS = "darwin"
	build.GOARCH = "arm64"
	if _, e = svc.RegisterArtifact(ctx, f.Principal, application.RegisterReleaseArtifactInput{SHA256: sha, Build: build}); e != nil {
		t.Fatal(e)
	}
	d := model.Definition{ID: "release-unit-rule", Name: "Release unit rule", SchemaVersion: model.ContractVersion, Kind: "analysis", Status: "published", Version: 1, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"device-climate-ventilation"}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "sum", Type: "aggregate", Params: map[string]any{"function": "avg"}}, {ID: "output", Type: "output"}}, Connections: []model.Connection{{From: "input", To: "sum"}, {From: "sum", To: "output"}}, Outputs: []model.Output{{Key: "temperature", Type: "number", NodeID: "output"}}}
	raw, _ := json.Marshal(d)
	ruleSHA, _ := releasebundle.ContentDigest(raw)
	ref := model.ConfigurationReference{Kind: "parameter", ID: p.ID, Version: p.Version, Digest: configcenter.ParameterDigest(p)}
	out.manifest = model.ReleaseManifest{SchemaVersion: releasebundle.Format, ID: "unit-release", Name: "Unit release", Program: "edge", Components: []model.ReleaseComponent{{ID: "program", Kind: "program", Version: build.Version, SHA256: sha, Format: releasebundle.ProgramFormat, Build: &build}, {ID: "configuration", Kind: "configuration", Version: "1", SHA256: ref.Digest, Format: releasebundle.ConfigurationFormat, Configuration: &ref}, {ID: d.ID, Kind: "rule", Version: "1", SHA256: ruleSHA, Format: model.ContractVersion, Content: raw, DependsOn: []model.ReleaseDependency{{ID: "configuration", Version: "1", SHA256: ref.Digest}}}}}
	out.release, e = svc.Create(ctx, f.Principal, application.CreateReleaseInput{RequestID: "create-release", Manifest: out.manifest})
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func (f *releaseFixture) deployment(t *testing.T, id string, batches [][]string) model.ReleaseDeployment {
	t.Helper()
	d, e := f.service.CreateDeployment(context.Background(), f.business.Principal, application.CreateReleaseDeploymentInput{ID: id, RequestID: "create-" + id, ReleaseID: f.release.ID, GroupID: "factory", Batches: batches, Reason: "release coordination unit verification"})
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func (f *releaseFixture) runtime(identity string) *model.ReleaseRuntime {
	n := f.nodes[identity].Identity
	program := releasebundle.Program(f.release.Manifest)
	r := &model.ReleaseRuntime{NodeID: n.NodeID, Program: "edge", ProcessInstanceID: "unit-process-" + identity, PID: 1234, StartedMS: time.Now().UnixMilli(), ProgramSHA256: program.SHA256, Build: *program.Build, ReleaseID: f.release.ID, ReleaseSHA256: f.release.SHA256, MigrationVersion: 11, PolicySHA256: strings.Repeat("d", 64), Healthy: true}
	for _, c := range releasebundle.ComponentsForNode(f.release.Manifest, n.NodeID) {
		r.Components = append(r.Components, model.ReleaseRuntimeComponent{ID: c.ID, Kind: c.Kind, Version: c.Version, SHA256: c.SHA256, AppliedSHA256: c.SHA256, AppliedVersion: 1})
	}
	return r
}
func (f *releaseFixture) report(t *testing.T, d model.ReleaseDeployment, identity, state string, seq int64) (model.ReleaseNodeReport, model.ReleaseTarget) {
	t.Helper()
	var generation int64
	for _, target := range d.Targets {
		if target.IdentityID == identity {
			generation = target.Generation
		}
	}
	in := model.ReleaseNodeReport{DeploymentID: d.ID, IdentityID: identity, NodeID: f.nodes[identity].Identity.NodeID, Generation: generation, Sequence: seq, ReleaseSHA256: f.release.SHA256, State: state}
	if state == "running" || state == "applied" {
		in.Runtime = f.runtime(identity)
	}
	target, e := f.service.Report(context.Background(), f.nodes[identity], in)
	if e != nil {
		t.Fatal(e)
	}
	return in, target
}

func checkReleaseCoordination(t *testing.T, pg bool) {
	f := newReleaseFixture(t, pg)
	ctx := context.Background()
	a, b := "workload-edge-a", "workload-edge-b"
	d := f.deployment(t, "deploy", [][]string{{a}, {b}})
	if got, e := f.service.Desired(ctx, f.nodes[b]); e != nil || got.Available {
		t.Fatal("later batch available", got, e)
	}
	f.report(t, d, a, "prepared", 1)
	first, _ := f.report(t, d, a, "running", 2)
	before, _ := f.service.Reports(ctx, f.business.Principal, d.ID)
	if _, e := f.service.Report(ctx, f.nodes[a], first); e != nil {
		t.Fatal("replay", e)
	}
	after, _ := f.service.Reports(ctx, f.business.Principal, d.ID)
	if len(before) != len(after) {
		t.Fatal("replay appended report")
	}
	changed := first
	changed.Reason = "different same sequence"
	if _, e := f.service.Report(ctx, f.nodes[a], changed); !errors.Is(e, store.ErrConflict) {
		t.Fatal("changed replay", e)
	}
	if e := f.service.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	reviewed, e := f.service.Deployment(ctx, f.business.Principal, d.ID)
	if e != nil || reviewed.CurrentBatch != 1 || reviewed.State != "active" {
		t.Fatal(reviewed, e)
	}
	for sequence := int64(3); sequence < 9; sequence++ {
		if _, e = f.service.Desired(ctx, f.nodes[a]); e != nil {
			t.Fatal(e)
		}
		f.report(t, d, a, "running", sequence)
	}
	current, _ := f.service.Deployment(ctx, f.business.Principal, d.ID)
	if current.Version != reviewed.Version {
		t.Fatal("heartbeat invalidated human version", current.Version, reviewed.Version)
	}
	paused, e := f.service.Action(ctx, f.business.Principal, d.ID, application.ReleaseDeploymentActionInput{RequestID: "pause", ExpectedVersion: reviewed.Version, Action: "pause", Reason: "reviewed before several heartbeats"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Action(ctx, f.business.Principal, d.ID, application.ReleaseDeploymentActionInput{RequestID: "stale", ExpectedVersion: reviewed.Version, Action: "resume", Reason: "stale operation"}); !errors.Is(e, store.ErrConflict) {
		t.Fatal(e)
	}
	d, e = f.service.Action(ctx, f.business.Principal, d.ID, application.ReleaseDeploymentActionInput{RequestID: "resume", ExpectedVersion: paused.Version, Action: "resume", Reason: "resume after reviewed pause"})
	if e != nil {
		t.Fatal(e)
	}
	f.report(t, d, b, "prepared", 1)
	f.report(t, d, b, "running", 2)
	if e = f.service.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	done, e := f.service.Deployment(ctx, f.business.Principal, d.ID)
	if e != nil || done.State != "completed" {
		t.Fatal(done, e)
	}
	old := f.nodes[a]
	newPrincipal, e := f.service.Nodes.ActivateInstance(ctx, f.credentials[a], "new-agent-a")
	if e != nil {
		t.Fatal(e)
	}
	fresh := first
	fresh.Sequence = 1
	fresh.Runtime = f.runtime(a)
	if _, e = f.service.Report(ctx, old, fresh); !errors.Is(e, identityAuthentication()) {
		t.Fatal("old instance accepted", e)
	}
	if _, e = f.service.Report(ctx, newPrincipal, fresh); e == nil {
		t.Fatal("new instance skipped prepare")
	}
	f.nodes[a] = newPrincipal
	f.report(t, d, a, "prepared", 1)
	f.report(t, d, a, "running", 2)
	// An online desired pull cannot extend the age of the actual runtime report.
	now := time.Now()
	f.business.Store.Now = func() time.Time { return now.Add(2 * time.Second) }
	if _, e = f.service.Desired(ctx, f.nodes[a]); e != nil {
		t.Fatal(e)
	}
	aged, e := f.service.Deployment(ctx, f.business.Principal, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, target := range aged.Targets {
		if target.ReportFresh {
			t.Fatal("stale runtime reported fresh", target)
		}
	}
	f.business.Store.Now = time.Now
	f.report(t, d, a, "running", 3)
	t.Log("fixed-content reports, batches, idempotency, heartbeat review version, instance fencing and independent runtime freshness verified")
}

func identityAuthentication() error                { return identity.ErrAuthentication }
func TestReleaseCoordinationSQLite(t *testing.T)   { checkReleaseCoordination(t, false) }
func TestReleaseCoordinationPostgres(t *testing.T) { checkReleaseCoordination(t, true) }

func TestReleaseRequestIdentityAndRuntimeContent(t *testing.T) {
	f := newReleaseFixture(t, false)
	ctx := context.Background()
	copy := f.manifest
	copy.ID = "second-release"
	if _, e := f.service.Create(ctx, f.business.Principal, application.CreateReleaseInput{RequestID: "create-release", Manifest: copy}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("request reused across identity", e)
	}
	copy = f.manifest
	copy.Name = "changed"
	if _, e := f.service.Create(ctx, f.business.Principal, application.CreateReleaseInput{RequestID: "other-create", Manifest: copy}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("release content changed", e)
	}
	d := f.deployment(t, "runtime", [][]string{{"workload-edge-a"}})
	f.report(t, d, "workload-edge-a", "prepared", 1)
	in := model.ReleaseNodeReport{DeploymentID: d.ID, IdentityID: "workload-edge-a", NodeID: "edge-a", Generation: d.Targets[0].Generation, Sequence: 2, ReleaseSHA256: f.release.SHA256, State: "running", Runtime: f.runtime("workload-edge-a")}
	for _, component := range []int{0, 1} {
		tampered := *in.Runtime
		tampered.Components = append([]model.ReleaseRuntimeComponent{}, tampered.Components...)
		tampered.Components[component].AppliedSHA256 = strings.Repeat("e", 64)
		attempt := in
		attempt.Runtime = &tampered
		if _, e := f.service.Report(ctx, f.nodes[in.IdentityID], attempt); e == nil {
			t.Fatal("incorrect applied component accepted", component)
		}
	}
	in.Generation++
	if _, e := f.service.Report(ctx, f.nodes[in.IdentityID], in); !errors.Is(e, store.ErrConflict) {
		t.Fatal("stale assignment", e)
	}
}

func TestReleaseDeploymentRequiresApplicableConfigurationAndCurrentUser(t *testing.T) {
	f := newReleaseFixture(t, false)
	ctx := context.Background()
	in := application.CreateReleaseDeploymentInput{ID: "reviewed-deployment", RequestID: "reviewed-request", ReleaseID: f.release.ID, GroupID: "factory", Batches: [][]string{{"workload-edge-a"}}, Reason: "verify applicable fixed configuration"}
	metadata := f.service.ConfigurationMetadata
	for _, change := range []func(*model.ConfigurationMetadata){
		func(m *model.ConfigurationMetadata) { m.NodeIDs = []string{"edge-b"} },
		func(m *model.ConfigurationMetadata) { m.Program = "gateway" },
		func(m *model.ConfigurationMetadata) { m.RequiredCapabilities = []string{"connector:missing"} },
	} {
		f.service.ConfigurationMetadata = func(ctx context.Context, refs []model.ConfigurationReference) ([]model.ConfigurationMetadata, error) {
			out, err := metadata(ctx, refs)
			if err == nil && len(out) > 0 {
				change(&out[0])
			}
			return out, err
		}
		if _, err := f.service.CreateDeployment(ctx, f.business.Principal, in); err == nil {
			t.Fatal("inapplicable fixed configuration accepted")
		}
		if _, err := f.business.Store.Get(ctx, "release_deployment", in.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("inapplicable deployment partially committed", err)
		}
	}
	f.service.ConfigurationMetadata = metadata
	d, err := f.service.CreateDeployment(ctx, f.business.Principal, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.service.CreateDeployment(ctx, f.business.Principal, in)
	if err != nil || again.ID != d.ID || again.Targets[0].Generation != d.Targets[0].Generation {
		t.Fatal("same request creates another assignment", again, err)
	}
	changed := in
	changed.ID = "another-deployment"
	if _, err = f.service.CreateDeployment(ctx, f.business.Principal, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed request reused", err)
	}
	if err = f.business.Identity.Logout(ctx, f.business.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Action(ctx, f.business.Principal, d.ID, application.ReleaseDeploymentActionInput{RequestID: "revoked-pause", ExpectedVersion: d.Version, Action: "pause", Reason: "session was revoked after review"}); !errors.Is(err, identity.ErrAuthentication) {
		t.Fatal("revoked captured principal mutated deployment", err)
	}
}

func TestTemplateEvolutionUsesExistingBindingsAndIndependentVersions(t *testing.T) {
	f := newReleaseFixture(t, false)
	ctx := context.Background()
	instance := f.business.Instances[0]
	batch, e := f.business.Business.PrepareTemplateBatch(ctx, f.business.Principal, model.TemplateBatchInput{ID: "source-template", RequestID: "source-template", GroupID: "factory", Instances: []model.TemplateInstance{instance}})
	if e != nil {
		t.Fatal(e)
	}
	in := application.EvolveTemplateBatchInput{ID: "evolution-2", RequestID: "evolve-once", ExpectedVersion: batch.Version, ReleaseID: "template-release-2", Name: "Template version two", ProgramSHA256: f.manifest.Components[0].SHA256, Targets: []application.TemplateEvolutionTarget{{InstanceID: instance.ID, TemplateVersion: 2, Parameters: map[string]any{"freshness_ms": 2500, "average_window_ms": 30000}}}}
	evolved, e := f.service.EvolveTemplateBatch(ctx, f.business.Principal, batch.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	if evolved.Instances[0].ID != instance.ID || evolved.Instances[0].DeviceID != instance.DeviceID || evolved.Bindings[0] != batch.Bindings[0] || evolved.Release.Manifest.Template.BatchID != batch.ID || evolved.Instances[0].TemplateVersion != 2 {
		t.Fatal(evolved)
	}
	if len(evolved.DefinitionIDs) != 14 || len(evolved.DeviceIDs) != 1 || len(evolved.NodeIDs) != 1 {
		t.Fatal(evolved)
	}
	for _, c := range evolved.Release.Manifest.Components {
		if c.Kind == "rule" {
			var d model.Definition
			if e = store.DecodeJSON(c.Content, &d); e != nil {
				t.Fatal(e)
			}
			if d.Kind == "strategy" && d.Policy.FreshnessMS != 2500 {
				t.Fatal(d)
			}
			if d.Kind == "analysis" && d.Selector.WindowMS != 30000 {
				t.Fatal(d)
			}
		}
	}
	again, e := f.service.EvolveTemplateBatch(ctx, f.business.Principal, batch.ID, in)
	if e != nil || again.Release.SHA256 != evolved.Release.SHA256 {
		t.Fatal(again, e)
	}
	changed := in
	changed.Name = "changed"
	if _, e = f.service.EvolveTemplateBatch(ctx, f.business.Principal, batch.ID, changed); !errors.Is(e, store.ErrConflict) {
		t.Fatal(e)
	}
	original, e := f.business.Business.TemplateBatch(ctx, f.business.Principal, batch.ID)
	if e != nil || original.Version != batch.Version || original.Input.Instances[0].TemplateVersion != 1 {
		t.Fatal(original, e)
	}
	if _, e = f.business.Store.Get(ctx, "definition", evolved.DefinitionIDs[0]); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("evolution published into cloud outside the release", e)
	}
	bad := in
	bad.ID = "failed-evolution"
	bad.ReleaseID = "failed-release"
	bad.RequestID = "bad-target"
	bad.Targets = []application.TemplateEvolutionTarget{{InstanceID: "another-instance", TemplateVersion: 2}}
	if _, e = f.service.EvolveTemplateBatch(ctx, f.business.Principal, batch.ID, bad); e == nil {
		t.Fatal("unknown instance accepted")
	}
	if _, e = f.business.Store.Get(ctx, "release", bad.ReleaseID); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("partial release committed", e)
	}
}

func TestReleaseConcurrentReconciliationPostgres(t *testing.T) {
	f := newReleaseFixture(t, true)
	ctx := context.Background()
	d := f.deployment(t, "concurrent", [][]string{{"workload-edge-a", "workload-edge-b"}})
	for _, id := range []string{"workload-edge-a", "workload-edge-b"} {
		f.report(t, d, id, "prepared", 1)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 60)
	for _, id := range []string{"workload-edge-a", "workload-edge-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for seq := int64(2); seq < 12; seq++ {
				in := model.ReleaseNodeReport{DeploymentID: d.ID, IdentityID: id, NodeID: f.nodes[id].Identity.NodeID, Generation: d.Targets[0].Generation, Sequence: seq, ReleaseSHA256: f.release.SHA256, State: "running", Runtime: f.runtime(id)}
				if _, e := f.service.Report(ctx, f.nodes[id], in); e != nil {
					failures <- fmt.Errorf("report %s: %w", id, e)
				}
			}
		}(id)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 15; i++ {
			if e := f.service.Reconcile(ctx); e != nil {
				failures <- e
			}
		}
	}()
	wg.Wait()
	close(failures)
	for e := range failures {
		t.Error(e)
	}
	if e := f.service.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	done, e := f.service.Deployment(ctx, f.business.Principal, d.ID)
	if e != nil || done.State != "completed" {
		t.Fatal(done, e)
	}
}

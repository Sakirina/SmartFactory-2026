package releaseagent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/internal/releaseruntime"
	"competition2026/product/platform/pkg/buildinfo"
	"competition2026/product/platform/pkg/model"
)

func TestPreparationFailureSeparatesUnavailableServicesFromInvalidContent(t *testing.T) {
	for _, tc := range []struct {
		err       error
		permanent bool
	}{
		{&HTTPError{Status: 422, Message: "artifact digest differs"}, true},
		{&HTTPError{Status: 400, Message: "fixed configuration invalid"}, true},
		{errors.New("executable build metadata differs"), true},
		{&HTTPError{Status: 503, Message: "authority offline"}, false},
		{&HTTPError{Status: 401, Message: "instance expired"}, false},
		{&HTTPError{Status: 409, Message: "assignment advanced"}, false},
		{context.DeadlineExceeded, false},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, false},
	} {
		e := preparationFailure("program", tc.err)
		var p *PreparationError
		if errors.As(e, &p) != tc.permanent {
			t.Fatalf("%v: permanent=%v", tc.err, tc.permanent)
		}
		if p != nil && p.ComponentID != "program" {
			t.Fatal(p)
		}
	}
}

func TestReleaseCachedCredentialRequiresFreshAuthorityAndExactContent(t *testing.T) {
	directory := t.TempDir()
	workload := model.WorkloadIdentity{ID: "release-a", NodeID: "edge-a", Program: "edge", Purpose: "release-runtime", Enabled: true, Version: 4, Generation: 2, InstanceID: "new-agent", InstanceEpoch: 3, Capabilities: []string{"credential.resolve", "connector:mqtt"}, ConnectorIDs: []string{"edge-a/mqtt-a"}}
	var mode atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["action"] != "validate" || request["capability"] != "credential.resolve" || request["expected_generation"] != float64(2) || request["expected_instance_epoch"] != float64(3) {
			t.Error("missing current authorization proof", request, err)
			http.Error(w, "invalid proof", 400)
			return
		}
		if mode.Load() == 1 {
			http.Error(w, "revoked", 403)
			return
		}
		current := workload
		if mode.Load() == 2 {
			current.Generation++
		}
		if mode.Load() == 3 {
			current.ConnectorIDs = nil
		}
		if mode.Load() == 4 {
			current.Purpose = "another-purpose"
		}
		if mode.Load() == 5 {
			current.Enabled = false
		}
		if mode.Load() == 6 {
			current.Capabilities = nil
		}
		json.NewEncoder(w).Encode(current)
	}))
	defer server.Close()
	agent := &Agent{Options: Options{Directory: directory, NodeID: "edge-a", CloudURL: server.URL}, Client: server.Client(), Workload: workload, InstanceID: workload.InstanceID, cipher: &identity.Manager{Master: make([]byte, 32)}}
	envelope := model.ConfigurationEnvelope{Reference: model.ConfigurationReference{Kind: "connector", ID: "edge-a/mqtt-a", Version: 2, Digest: strings.Repeat("b", 64)}, NodeID: "edge-a", Program: "edge", Purpose: "release-runtime", CredentialRef: "edge-a/mqtt-a:2", Connector: &model.ConnectorConfiguration{ID: "edge-a/mqtt-a", EdgeID: "edge-a", Protocol: "mqtt", Version: 2}}
	release := model.Release{ID: "release", Manifest: model.ReleaseManifest{ID: "release", Program: "edge", Components: []model.ReleaseComponent{{ID: "mqtt", Kind: "configuration", Configuration: &envelope.Reference}}}}
	release.SHA256 = releasebundle.ManifestDigest(release.Manifest)
	secret := json.RawMessage(`{"password":"retained-secret"}`)
	cached, err := releaseruntime.Seal(releaseruntime.Staged{NodeID: "edge-a", Release: release, Configurations: []model.ConfigurationEnvelope{envelope}, Private: map[string]json.RawMessage{envelope.CredentialRef: secret}, CredentialGeneration: 2}, agent.cipher)
	if err != nil {
		t.Fatal(err)
	}
	if err = WritePrivate(filepath.Join(directory, "releases", release.SHA256+".json"), cached); err != nil {
		t.Fatal(err)
	}
	desired := model.ReleaseDesired{Release: &release}
	for _, status := range []int{401, 403, 409} {
		if _, ok := agent.cachedCredential(context.Background(), desired, envelope, &HTTPError{Status: status}); ok {
			t.Fatal("authorization or content rejection used cache", status)
		}
	}
	if got, ok := agent.cachedCredential(context.Background(), desired, envelope, &HTTPError{Status: 503}); !ok || string(got) != string(secret) {
		t.Fatal("authorized exact cache was not recovered", string(got), ok)
	}
	for _, value := range []int64{1, 2, 3, 4, 5, 6} {
		mode.Store(value)
		if _, ok := agent.cachedCredential(context.Background(), desired, envelope, &HTTPError{Status: 503}); ok {
			t.Fatal("revoked or replaced authorization reused cache", value)
		}
	}
	mode.Store(0)
	agent.State.ReleaseSHA256 = release.SHA256
	newRelease := release
	newRelease.ID = "new-program-release"
	newRelease.Manifest.ID = newRelease.ID
	newRelease.SHA256 = releasebundle.ManifestDigest(newRelease.Manifest)
	desired.Release = &newRelease
	if got, ok := agent.cachedCredential(context.Background(), desired, envelope, &HTTPError{Status: 503}); !ok || string(got) != string(secret) {
		t.Fatal("new authorized release could not reuse its unchanged fixed envelope", ok)
	}
	agent.Pending = &State{ReleaseSHA256: release.SHA256}
	agent.State.ReleaseSHA256 = ""
	if _, ok := agent.cachedCredential(context.Background(), desired, envelope, &HTTPError{Status: 503}); !ok {
		t.Fatal("authorized prepared cache could not restore the same fixed envelope")
	}
	for _, change := range []func(*model.ConfigurationEnvelope){
		func(e *model.ConfigurationEnvelope) { e.Reference.Version++ },
		func(e *model.ConfigurationEnvelope) { e.Reference.Digest = strings.Repeat("c", 64) },
		func(e *model.ConfigurationEnvelope) { e.Reference.ID = "another-connector" },
		func(e *model.ConfigurationEnvelope) { e.CredentialRef = "another-credential" },
		func(e *model.ConfigurationEnvelope) { e.NodeID = "another-node" },
		func(e *model.ConfigurationEnvelope) { e.Program = "cloud" },
		func(e *model.ConfigurationEnvelope) { e.Purpose = "another-purpose" },
		func(e *model.ConfigurationEnvelope) { e.Dynamic = true },
	} {
		changed := envelope
		change(&changed)
		if _, ok := agent.cachedCredential(context.Background(), desired, changed, &HTTPError{Status: 503}); ok {
			t.Fatal("changed fixed envelope reused cache", changed.Reference)
		}
	}
	agent.Workload.Generation++
	if _, ok := agent.cachedCredential(context.Background(), desired, envelope, &HTTPError{Status: 503}); ok {
		t.Fatal("new credential generation reused old cache")
	}
}

func TestAgentContinuesOnlineAfterVerifiedCachedProgramExits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var activations, desiredReads atomic.Int64
	workload := model.WorkloadIdentity{ID: "release-a", NodeID: "edge-a", Program: "edge", Purpose: "release-runtime", Enabled: true, Capabilities: []string{"release"}, Generation: 1, InstanceID: "new-agent", InstanceEpoch: 2}
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/authority/workload" {
			activations.Add(1)
			json.NewEncoder(w).Encode(workload)
			return
		}
		if r.URL.Path == "/internal/releases/desired" {
			desiredReads.Add(1)
			json.NewEncoder(w).Encode(model.ReleaseDesired{})
			cancel()
			return
		}
		http.NotFound(w, r)
	}))
	defer cloud.Close()
	local := httptest.NewServer(http.NotFoundHandler())
	defer local.Close()
	a := cachedExitingAgent(t)
	a.Options.CloudURL, a.Options.ConfigURL, a.Options.Listen = cloud.URL, cloud.URL, strings.TrimPrefix(local.URL, "http://")
	a.Client, a.InstanceID = cloud.Client(), workload.InstanceID
	before, err := os.ReadFile(a.State.PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if activations.Load() != 1 || desiredReads.Load() != 1 {
		t.Fatalf("online recovery was not attempted: activate=%d desired=%d", activations.Load(), desiredReads.Load())
	}
	if a.restoreFailure == "" {
		t.Fatal("cached application failure was not retained")
	}
	after, err := os.ReadFile(a.State.PayloadPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("recovery changed the original release cache", err)
	}
	if _, err = os.Stat(filepath.Join(a.Options.Directory, "restore-status.json")); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRejectsChangedCacheAndArtifactBeforeOnlineRecovery(t *testing.T) {
	for _, kind := range []string{"payload", "artifact", "manifest", "identity"} {
		t.Run(kind, func(t *testing.T) {
			a := cachedExitingAgent(t)
			var requests atomic.Int64
			cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.Error(w, "unexpected", 500) }))
			defer cloud.Close()
			local := httptest.NewServer(http.NotFoundHandler())
			defer local.Close()
			a.Options.CloudURL, a.Options.Listen, a.Client = cloud.URL, strings.TrimPrefix(local.URL, "http://"), cloud.Client()
			switch kind {
			case "payload":
				os.WriteFile(a.State.PayloadPath, []byte(`{"format":"changed"}`), 0600)
			case "artifact":
				path := filepath.Join(a.Options.Directory, "artifacts", a.State.ProgramSHA256)
				os.Chmod(path, 0600)
				os.WriteFile(path, []byte("changed"), 0600)
			case "manifest":
				staged, err := a.cachedRelease(a.State.ReleaseSHA256)
				if err != nil {
					t.Fatal(err)
				}
				staged.Release.Manifest.Name = "changed"
				cached, err := releaseruntime.Seal(staged, a.cipher)
				if err != nil {
					t.Fatal(err)
				}
				if err = WritePrivate(a.State.PayloadPath, cached); err != nil {
					t.Fatal(err)
				}
			case "identity":
				a.State.ReleaseID = "another-release"
			}
			if err := a.Run(context.Background()); err == nil || requests.Load() != 0 {
				t.Fatalf("invalid persisted material entered online recovery: %v requests=%d", err, requests.Load())
			}
		})
	}
}

func TestAgentWaitsForNewAssignmentAfterReportingCachedStartupFailure(t *testing.T) {
	a := cachedExitingAgent(t)
	staged, err := a.cachedRelease(a.State.ReleaseSHA256)
	if err != nil {
		t.Fatal(err)
	}
	a.State.DeploymentID, a.State.Generation = "original-deployment", 3
	a.restoreFailure = "cached release could not start: application exited"
	a.Workload = model.WorkloadIdentity{ID: "release-a", NodeID: "edge-a", Program: "edge", Purpose: "release-runtime", InstanceEpoch: 4}
	desired := model.ReleaseDesired{Available: true, DeploymentID: a.State.DeploymentID, Generation: a.State.Generation, Release: &staged.Release}
	var reads, reports, otherRequests atomic.Int64
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/releases/desired":
			reads.Add(1)
			json.NewEncoder(w).Encode(desired)
		case "/internal/releases/reports":
			var report model.ReleaseNodeReport
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Error(err)
			}
			if report.State != "failed" || report.ComponentID != "program" || report.Generation == a.State.Generation && report.Reason != a.restoreFailure {
				t.Errorf("unexpected retained failure report: %+v", report)
			}
			reports.Add(1)
			json.NewEncoder(w).Encode(model.ReleaseTarget{})
		default:
			otherRequests.Add(1)
			http.Error(w, "same failed assignment must not prepare or start again", http.StatusInternalServerError)
		}
	}))
	defer cloud.Close()
	a.Client, a.Options.CloudURL, a.Options.ConfigURL = cloud.Client(), cloud.URL, cloud.URL
	a.Options.Listen = strings.TrimPrefix(cloud.URL, "http://")
	for range 3 {
		if err := a.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if reads.Load() != 3 || reports.Load() != 1 || otherRequests.Load() != 0 {
		t.Fatalf("failed assignment retried: desired=%d reports=%d other=%d", reads.Load(), reports.Load(), otherRequests.Load())
	}
	desired.Generation++
	if err := a.Tick(context.Background()); err == nil || otherRequests.Load() == 0 {
		t.Fatal("an authorized retry generation did not attempt preparation")
	}
}

func TestAgentRestartRetainsFailedAssignmentWithoutStartingIt(t *testing.T) {
	a := cachedExitingAgent(t)
	a.State.DeploymentID, a.State.Generation = "original-deployment", 3
	local := httptest.NewServer(http.NotFoundHandler())
	defer local.Close()
	a.Client, a.Options.Listen = local.Client(), strings.TrimPrefix(local.URL, "http://")
	status := restoreStatus{InstanceID: "prior-agent", DeploymentID: a.State.DeploymentID, Generation: a.State.Generation, ReleaseSHA256: a.State.ReleaseSHA256, Error: "cached application exited", State: "awaiting_authorized_release"}
	if err := WritePrivate(filepath.Join(a.Options.Directory, "restore-status.json"), status); err != nil {
		t.Fatal(err)
	}
	if err := a.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.restoreFailure != status.Error || a.child != nil {
		t.Fatal("a restarted supervisor attempted the previously failed assignment")
	}
	if _, err := os.Stat(filepath.Join(a.Options.Directory, "program.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a previously failed child was launched again", err)
	}
}

func TestAgentRejectsUnauthenticatedExistingProcessBeforeRecovery(t *testing.T) {
	a := cachedExitingAgent(t)
	a.State.Runtime.PID = os.Getpid()
	var requests atomic.Int64
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.NotFound(w, r) }))
	defer cloud.Close()
	local := httptest.NewServer(http.NotFoundHandler())
	defer local.Close()
	a.Client, a.Options.Listen, a.Options.CloudURL = cloud.Client(), strings.TrimPrefix(local.URL, "http://"), cloud.URL
	if err := a.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "runtime cannot be authenticated") || requests.Load() != 0 || a.child != nil {
		t.Fatalf("unverified process entered recovery: %v online=%d", err, requests.Load())
	}
}

func cachedExitingAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	digest, err := buildinfo.ExecutableSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = releasebundle.WriteArtifact(filepath.Join(dir, "artifacts"), digest, file); err != nil {
		t.Fatal(err)
	}
	build := buildinfo.Current("edge")
	definition := model.Definition{ID: "restore-rule", Name: "Restore rule", SchemaVersion: model.ContractVersion, Kind: "analysis", Status: "published", Version: 1, GroupID: "factory", Selector: model.Selector{DeviceIDs: []string{"device"}, Keys: []string{"temperature"}}, Nodes: []model.Node{{ID: "input", Type: "input"}, {ID: "sum", Type: "aggregate", Params: map[string]any{"function": "avg"}}, {ID: "output", Type: "output"}}, Connections: []model.Connection{{From: "input", To: "sum"}, {From: "sum", To: "output"}}, Outputs: []model.Output{{Key: "temperature", Type: "number", NodeID: "output"}}}
	raw, _ := json.Marshal(definition)
	ruleDigest, _ := releasebundle.ContentDigest(raw)
	ref := model.ConfigurationReference{Kind: "parameter", ID: "heartbeat.interval_ms", Version: 1, Digest: strings.Repeat("a", 64)}
	manifest := model.ReleaseManifest{SchemaVersion: releasebundle.Format, ID: "cached-release", Name: "Cached release", Program: "edge", Components: []model.ReleaseComponent{{ID: "program", Kind: "program", Version: build.Version, SHA256: digest, Format: releasebundle.ProgramFormat, Build: &build}, {ID: "configuration", Kind: "configuration", Version: "1", SHA256: ref.Digest, Format: releasebundle.ConfigurationFormat, Configuration: &ref}, {ID: definition.ID, Kind: "rule", Version: "1", SHA256: ruleDigest, Format: model.ContractVersion, Content: raw}}}
	release := model.Release{ID: manifest.ID, Manifest: manifest, SHA256: releasebundle.ManifestDigest(manifest)}
	a := &Agent{Options: Options{Directory: dir, NodeID: "edge-a", Listen: "127.0.0.1:1", PollInterval: 10 * time.Millisecond}, cipher: &identity.Manager{Master: make([]byte, 32)}, State: State{ReleaseID: release.ID, ReleaseSHA256: release.SHA256, ProgramSHA256: digest, PayloadPath: filepath.Join(dir, "releases", release.SHA256+".json")}}
	cached, err := releaseruntime.Seal(releaseruntime.Staged{NodeID: "edge-a", Release: release, Configurations: []model.ConfigurationEnvelope{{Reference: ref, NodeID: "edge-a", Program: "edge", Purpose: "release-runtime"}}, CredentialGeneration: 1}, a.cipher)
	if err != nil {
		t.Fatal(err)
	}
	if err = WritePrivate(a.State.PayloadPath, cached); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRuntimeConfigurationRetainsOriginalStorageAndIntegrations(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "workload.token")
	if err := os.WriteFile(token, []byte("test-workload-credential-at-least-32-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(dir, "original")
	runtime := filepath.Join(dir, "runtime.json")
	expected := RuntimeOptions{Database: filepath.Join(original, "edge.db"), MasterKeyFile: filepath.Join(original, "master.key"), StaticDirectory: filepath.Join(original, "static"), Environment: map[string]string{"SF_SYNC_URL": "https://cloud.example/sync", "SF_NATS_URL": "tls://nats.example:4222", "SF_TB_URL": "http://127.0.0.1:8080", "SF_DATATRANSFER_ADDRESS": "127.0.0.1:19090", "OTEL_SERVICE_NAME": "production-edge"}}
	if err := WritePrivate(runtime, expected); err != nil {
		t.Fatal(err)
	}
	agent, err := New(Options{CloudURL: "http://127.0.0.1:10001", ConfigURL: "http://127.0.0.1:10002", Directory: filepath.Join(dir, "supervisor"), NodeID: "edge-a", Listen: "127.0.0.1:10003", TokenFile: token, RuntimeConfigFile: runtime})
	if err != nil {
		t.Fatal(err)
	}
	if agent.RuntimeOptions.Database != expected.Database || agent.RuntimeOptions.MasterKeyFile != expected.MasterKeyFile || agent.RuntimeOptions.Environment["SF_NATS_URL"] != expected.Environment["SF_NATS_URL"] {
		t.Fatal(agent.RuntimeOptions)
	}
	if _, err = os.Stat(expected.MasterKeyFile); err != nil {
		t.Fatal(err)
	}
	expected.Environment["SF_RELEASE_PAYLOAD"] = "arbitrary"
	if err = WritePrivate(runtime, expected); err != nil {
		t.Fatal(err)
	}
	if _, err = New(Options{CloudURL: "http://127.0.0.1:10001", ConfigURL: "http://127.0.0.1:10002", Directory: filepath.Join(dir, "supervisor"), NodeID: "edge-a", Listen: "127.0.0.1:10003", TokenFile: token, RuntimeConfigFile: runtime}); err == nil {
		t.Fatal("release payload override accepted")
	}
}

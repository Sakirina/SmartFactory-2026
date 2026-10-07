// Package releaseagent manages real application processes from fixed releases.
// It retains the application database and a private verified release cache.
package releaseagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"competition2026/product/platform/internal/cloudsync"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/releasebundle"
	"competition2026/product/platform/internal/releaseruntime"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Options struct {
	CloudURL              string
	ConfigURL             string
	Directory             string
	NodeID                string
	TokenFile             string
	Listen                string
	BootstrapPasswordFile string
	TLSCA                 string
	TLSCertificate        string
	TLSKey                string
	DataTransferAddress   string
	Seed                  bool
	PollInterval          time.Duration
	RuntimeConfigFile     string
}
type RuntimeOptions struct {
	Database        string            `json:"database"`
	MasterKeyFile   string            `json:"master_key_file"`
	StaticDirectory string            `json:"static_directory"`
	Environment     map[string]string `json:"environment"`
}
type State struct {
	DeploymentID       string               `json:"deployment_id"`
	Generation         int64                `json:"generation"`
	ReleaseID          string               `json:"release_id"`
	ReleaseSHA256      string               `json:"release_sha256"`
	ProgramSHA256      string               `json:"program_sha256"`
	PayloadPath        string               `json:"payload_path"`
	Runtime            model.ReleaseRuntime `json:"runtime"`
	AgentInstanceEpoch int64                `json:"agent_instance_epoch"`
}
type RuntimeResponse struct {
	Runtime        model.ReleaseRuntime                `json:"runtime"`
	Configurations []configcenter.RuntimeConfiguration `json:"configurations"`
}
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message) }

type PreparationError struct {
	ComponentID string
	Cause       error
}

// applicationStartError is returned only after the verified executable starts.
// A new authorized release can recover an application-level startup failure.
type applicationStartError struct{ cause error }

func (e *applicationStartError) Error() string { return e.cause.Error() }
func (e *applicationStartError) Unwrap() error { return e.cause }

func (e *PreparationError) Error() string { return e.Cause.Error() }
func (e *PreparationError) Unwrap() error { return e.Cause }
func preparationFailure(component string, err error) error {
	if err == nil {
		return nil
	}
	var network net.Error
	var httpError *HTTPError
	if errors.As(err, &network) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.As(err, &httpError) && (httpError.Status == 401 || httpError.Status == 403 || httpError.Status == 409 || httpError.Status == 408 || httpError.Status == 429 || httpError.Status >= 500) {
		return err
	}
	return &PreparationError{ComponentID: component, Cause: err}
}

type Transition struct {
	DeploymentID         string `json:"deployment_id"`
	Generation           int64  `json:"generation"`
	OldPID               int    `json:"old_pid"`
	OldProcessInstanceID string `json:"old_process_instance_id"`
	OldExitedMS          int64  `json:"old_exited_ms"`
	NewPID               int    `json:"new_pid"`
	NewProcessInstanceID string `json:"new_process_instance_id"`
	NewReadyMS           int64  `json:"new_ready_ms"`
}

type restoreStatus struct {
	InstanceID    string `json:"instance_id"`
	AtMS          int64  `json:"at_ms"`
	DeploymentID  string `json:"deployment_id"`
	Generation    int64  `json:"generation"`
	ReleaseSHA256 string `json:"release_sha256"`
	Error         string `json:"error"`
	State         string `json:"state"`
}

type Agent struct {
	Options                Options
	RuntimeOptions         RuntimeOptions
	Client                 *http.Client
	InstanceID             string
	Workload               model.WorkloadIdentity
	token                  string
	localToken             string
	password               string
	cipher                 *identity.Manager
	sequence               int64
	configurationSequence  int64
	State                  State
	Pending                *State
	restoreFailure         string
	restoreFailureReported bool
	mu                     sync.Mutex
	child                  *exec.Cmd
	childDone              chan error
}

func privateRead(path string) ([]byte, error) {
	info, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8<<20 {
		return nil, errors.New("private material must be an owner-only regular file")
	}
	return os.ReadFile(path)
}
func WritePrivate(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".state-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, e = f.Write(append(b, '\n')); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func loadOrCreateSecret(path string) (string, error) {
	b, e := privateRead(path)
	if e == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	secret := identity.ID() + identity.ID()
	if e = os.WriteFile(path, []byte(secret), 0600); e != nil {
		return "", e
	}
	return secret, nil
}

func New(o Options) (*Agent, error) {
	if o.Directory == "" || o.NodeID == "" || o.Listen == "" || o.CloudURL == "" || o.ConfigURL == "" {
		return nil, errors.New("release agent requires directory, node, listen, cloud and configuration URLs")
	}
	for _, endpoint := range []string{o.CloudURL, o.ConfigURL} {
		u, e := url.Parse(endpoint)
		if e != nil || u.User != nil || (u.Scheme != "https" && (u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback())) {
			return nil, errors.New("release services require HTTPS or an isolated loopback address")
		}
	}
	if e := os.MkdirAll(o.Directory, 0700); e != nil {
		return nil, e
	}
	raw, e := privateRead(o.TokenFile)
	if e != nil {
		return nil, e
	}
	a := &Agent{Options: o, InstanceID: identity.ID(), token: strings.TrimSpace(string(raw)), Client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	a.RuntimeOptions = RuntimeOptions{Database: filepath.Join(o.Directory, "node.db"), MasterKeyFile: filepath.Join(o.Directory, "node.key"), Environment: map[string]string{}}
	if o.RuntimeConfigFile != "" {
		content, e := privateRead(o.RuntimeConfigFile)
		if e != nil {
			return nil, e
		}
		if e = store.DecodeJSON(content, &a.RuntimeOptions); e != nil {
			return nil, e
		}
		if a.RuntimeOptions.Database == "" || !filepath.IsAbs(a.RuntimeOptions.MasterKeyFile) {
			return nil, errors.New("runtime configuration requires a database and absolute master key path")
		}
		if !strings.Contains(a.RuntimeOptions.Database, "://") && !strings.HasPrefix(a.RuntimeOptions.Database, "file:") && !filepath.IsAbs(a.RuntimeOptions.Database) {
			return nil, errors.New("runtime SQLite database requires an absolute path")
		}
		for name := range a.RuntimeOptions.Environment {
			if !strings.HasPrefix(name, "SF_") && !strings.HasPrefix(name, "OTEL_") {
				return nil, errors.New("runtime environment accepts SF_ and OTEL_ application settings")
			}
			if slices.Contains([]string{"SF_RELEASE_PAYLOAD", "SF_NODE_ID", "SF_DATABASE", "SF_MASTER_KEY_FILE", "SF_LISTEN"}, name) {
				return nil, fmt.Errorf("runtime setting %s is supplied by the release supervisor", name)
			}
		}
	}
	if o.TLSCA != "" {
		a.Client, e = cloudsync.NewHTTPClient(o.TLSCA, o.TLSCertificate, o.TLSKey)
		if e != nil {
			return nil, e
		}
		a.Client.Timeout = 15 * time.Second
		a.Client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	a.localToken, e = loadOrCreateSecret(filepath.Join(o.Directory, "runtime-token"))
	if e != nil {
		return nil, e
	}
	if configured := a.RuntimeOptions.Environment["SF_SERVICE_TOKEN"]; configured != "" {
		a.localToken = configured
	}
	key, e := identity.LoadMasterKey(a.RuntimeOptions.MasterKeyFile)
	if e != nil {
		return nil, e
	}
	a.cipher = &identity.Manager{Master: key}
	if o.BootstrapPasswordFile != "" {
		raw, e = privateRead(o.BootstrapPasswordFile)
		if e != nil {
			return nil, e
		}
		a.password = strings.TrimSpace(string(raw))
	} else {
		a.password, e = loadOrCreateSecret(filepath.Join(o.Directory, "bootstrap-password"))
		if e != nil {
			return nil, e
		}
	}
	if raw, e = privateRead(filepath.Join(o.Directory, "active.json")); e == nil {
		if e = store.DecodeJSON(raw, &a.State); e != nil {
			return nil, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if raw, e = privateRead(filepath.Join(o.Directory, "pending.json")); e == nil {
		var pending State
		if e = store.DecodeJSON(raw, &pending); e != nil {
			return nil, e
		}
		a.Pending = &pending
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	return a, nil
}

func (a *Agent) saveActive() error {
	if e := WritePrivate(filepath.Join(a.Options.Directory, "active.json"), a.State); e != nil {
		return e
	}
	if e := os.Remove(filepath.Join(a.Options.Directory, "pending.json")); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	a.Pending = nil
	return nil
}

func (a *Agent) call(ctx context.Context, base, method, path string, body, out any) error {
	var raw []byte
	var e error
	if body != nil {
		raw, e = json.Marshal(body)
		if e != nil {
			return e
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-SF-Instance-ID", a.InstanceID)
	response, e := a.Client.Do(req)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &HTTPError{Status: response.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(response.Body, 2<<20))
		return e
	}
	return store.DecodeJSONReader(io.LimitReader(response.Body, 8<<20), out)
}
func (a *Agent) Activate(ctx context.Context) error {
	var w model.WorkloadIdentity
	e := a.call(ctx, a.Options.CloudURL, http.MethodPost, "/internal/authority/workload", map[string]any{"action": "activate"}, &w)
	if e == nil {
		if w.NodeID != a.Options.NodeID || !slices.Contains(w.Capabilities, "release") {
			return errors.New("authenticated workload differs from agent node or purpose")
		}
		a.Workload = w
	}
	return e
}

func (a *Agent) Runtime(ctx context.Context) (RuntimeResponse, error) {
	var result RuntimeResponse
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	address := a.Options.Listen
	if host, port, e := net.SplitHostPort(address); e == nil {
		if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
			address = net.JoinHostPort("127.0.0.1", port)
		}
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/internal/releases/runtime", nil)
	if e != nil {
		return result, e
	}
	req.Header.Set("Authorization", "Bearer "+a.localToken)
	client := &http.Client{Timeout: 2 * time.Second}
	response, e := client.Do(req)
	if e != nil {
		return result, e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return result, fmt.Errorf("application runtime is unavailable: HTTP %d", response.StatusCode)
	}
	e = store.DecodeJSONReader(io.LimitReader(response.Body, 2<<20), &result)
	return result, e
}

func (a *Agent) download(ctx context.Context, digest string) error {
	root := filepath.Join(a.Options.Directory, "artifacts")
	if _, e := releasebundle.VerifyArtifact(root, digest); e == nil {
		return nil
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.Options.CloudURL, "/")+"/internal/releases/artifacts/"+digest, nil)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-SF-Instance-ID", a.InstanceID)
	response, e := a.Client.Do(req)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return &HTTPError{Status: response.StatusCode, Message: "artifact download failed"}
	}
	_, e = releasebundle.WriteArtifact(root, digest, response.Body)
	return e
}

func (a *Agent) prepare(ctx context.Context, d model.ReleaseDesired) (payload string, resultErr error) {
	componentID := ""
	defer func() { resultErr = preparationFailure(componentID, resultErr) }()
	if d.Release == nil {
		return "", errors.New("desired release is missing")
	}
	v := releasebundle.Validate(ctx, d.Release.Manifest)
	if !v.Valid || v.SHA256 != d.Release.SHA256 {
		return "", errors.New("desired release content does not match its digest")
	}
	program := releasebundle.Program(d.Release.Manifest)
	componentID = program.ID
	if e := a.download(ctx, program.SHA256); e != nil {
		return "", e
	}
	path, e := releasebundle.ArtifactPath(filepath.Join(a.Options.Directory, "artifacts"), program.SHA256)
	if e != nil {
		return "", e
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	out, e := exec.CommandContext(probeCtx, path, "-build-info").Output()
	cancel()
	if e != nil {
		return "", fmt.Errorf("executable build inspection failed: %w", e)
	}
	var info struct {
		Build  model.ProgramBuild `json:"build"`
		SHA256 string             `json:"sha256"`
	}
	if e = store.DecodeJSON(out, &info); e != nil {
		return "", e
	}
	if info.SHA256 != program.SHA256 || store.Hash(info.Build) != store.Hash(program.Build) {
		return "", errors.New("executable build information differs from manifest")
	}
	if current, e := a.Runtime(ctx); e == nil {
		if e = releasebundle.CheckDatabase(info.Build, current.Runtime.MigrationVersion); e != nil {
			return "", e
		}
	}
	refs := releasebundle.ReferencesForNode(d.Release.Manifest, a.Options.NodeID)
	componentID = ""
	target := model.ReleaseConfigurationTarget{DeploymentID: d.DeploymentID, Generation: d.Generation, References: refs}
	if e = a.call(ctx, a.Options.ConfigURL, http.MethodPost, "/internal/config/v2/release-target", target, nil); e != nil {
		return "", e
	}
	var envelopes []model.ConfigurationEnvelope
	if e = a.call(ctx, a.Options.ConfigURL, http.MethodPost, "/internal/config/v2/versions", map[string]any{"references": refs}, &envelopes); e != nil {
		return "", e
	}
	if len(envelopes) != len(refs) {
		return "", errors.New("configuration center returned an incomplete release")
	}
	private := map[string]json.RawMessage{}
	for i, envelope := range envelopes {
		for _, c := range d.Release.Manifest.Components {
			if c.Configuration != nil && *c.Configuration == refs[i] {
				componentID = c.ID
				break
			}
		}
		if envelope.Reference != refs[i] || envelope.NodeID != a.Workload.NodeID || envelope.Program != a.Workload.Program || envelope.Purpose != a.Workload.Purpose {
			return "", errors.New("configuration envelope differs from authenticated target")
		}
		if envelope.CredentialRef != "" {
			request := model.CredentialResolution{Reference: envelope.Reference, CredentialRef: envelope.CredentialRef, Purpose: a.Workload.Purpose}
			var payload model.CredentialPayload
			if e = a.call(ctx, a.Options.ConfigURL, http.MethodPost, "/internal/config/v2/credentials/resolve", request, &payload); e != nil {
				if retained, ok := a.cachedCredential(ctx, d, envelope, e); ok {
					private[envelope.CredentialRef] = retained
					continue
				}
				return "", e
			}
			if payload.Reference != envelope.Reference || payload.CredentialRef != envelope.CredentialRef {
				return "", errors.New("credential resolver returned another fixed version")
			}
			private[envelope.CredentialRef] = payload.Payload
		}
	}
	payloadPath := filepath.Join(a.Options.Directory, "releases", d.Release.SHA256+".json")
	sealed, e := releaseruntime.Seal(releaseruntime.Staged{NodeID: a.Options.NodeID, Release: *d.Release, Configurations: envelopes, Private: private, CredentialGeneration: a.Workload.Generation}, a.cipher)
	if e != nil {
		return "", e
	}
	if e = WritePrivate(payloadPath, sealed); e != nil {
		return "", e
	}
	return payloadPath, nil
}

// A failed replacement can leave its own credential source process offline.
// The current desired release may reuse an identical fixed envelope from its
// active or prepared cache after fresh identity and scope validation.
func (a *Agent) cachedCredential(ctx context.Context, desired model.ReleaseDesired, envelope model.ConfigurationEnvelope, unavailable error) (json.RawMessage, bool) {
	var network net.Error
	var httpError *HTTPError
	temporary := errors.As(unavailable, &network)
	if errors.As(unavailable, &httpError) {
		temporary = httpError.Status == 408 || httpError.Status == 429 || httpError.Status >= 500
	}
	if !temporary || desired.Release == nil || a.cipher == nil || desired.Release.SHA256 != releasebundle.ManifestDigest(desired.Release.Manifest) || desired.Release.Manifest.Program != a.Workload.Program || envelope.NodeID != a.Workload.NodeID || envelope.Program != a.Workload.Program || envelope.Purpose != a.Workload.Purpose || !slices.Contains(releasebundle.ReferencesForNode(desired.Release.Manifest, a.Options.NodeID), envelope.Reference) {
		return nil, false
	}
	candidates := []string{desired.Release.SHA256, a.State.ReleaseSHA256}
	if a.Pending != nil {
		candidates = append(candidates, a.Pending.ReleaseSHA256)
	}
	var retained json.RawMessage
	for _, digest := range candidates {
		staged, err := a.cachedRelease(digest)
		if err != nil || staged.CredentialGeneration == 0 || staged.CredentialGeneration != a.Workload.Generation || staged.Release.Manifest.Program != a.Workload.Program || !slices.Contains(releasebundle.ReferencesForNode(staged.Release.Manifest, a.Options.NodeID), envelope.Reference) {
			continue
		}
		index := slices.IndexFunc(staged.Configurations, func(item model.ConfigurationEnvelope) bool { return store.Hash(item) == store.Hash(envelope) })
		if index >= 0 && len(staged.Private[envelope.CredentialRef]) > 0 {
			retained = staged.Private[envelope.CredentialRef]
			break
		}
	}
	if len(retained) == 0 {
		return nil, false
	}
	var current model.WorkloadIdentity
	err := a.call(ctx, a.Options.CloudURL, http.MethodPost, "/internal/authority/workload", map[string]any{"action": "validate", "capability": "credential.resolve", "expected_identity_version": a.Workload.Version, "expected_generation": a.Workload.Generation, "expected_instance_epoch": a.Workload.InstanceEpoch}, &current)
	if err != nil || !current.Enabled || current.ID != a.Workload.ID || current.NodeID != a.Options.NodeID || current.Program != envelope.Program || current.Purpose != envelope.Purpose || current.Version != a.Workload.Version || current.Generation != a.Workload.Generation || current.InstanceID != a.InstanceID || current.InstanceEpoch != a.Workload.InstanceEpoch || !slices.Contains(current.Capabilities, "credential.resolve") {
		return nil, false
	}
	if envelope.Reference.Kind == "parameter" && !slices.Contains(current.ParameterIDs, envelope.Reference.ID) || envelope.Reference.Kind == "connector" && (!slices.Contains(current.ConnectorIDs, envelope.Reference.ID) || envelope.Connector == nil || !slices.Contains(current.Capabilities, "connector:"+envelope.Connector.Protocol)) {
		return nil, false
	}
	return append(json.RawMessage{}, retained...), true
}

func (a *Agent) cachedRelease(digest string) (releaseruntime.Staged, error) {
	var staged releaseruntime.Staged
	if !releasebundle.ValidDigest(digest) || a.cipher == nil {
		return staged, errors.New("cached release identity is invalid")
	}
	raw, err := privateRead(filepath.Join(a.Options.Directory, "releases", digest+".json"))
	if err != nil {
		return staged, err
	}
	var cache releaseruntime.Cached
	if err = store.DecodeJSON(raw, &cache); err != nil {
		return staged, err
	}
	staged, err = releaseruntime.Unseal(cache, a.Options.NodeID, a.cipher)
	if err != nil {
		return staged, err
	}
	if staged.Release.SHA256 != digest || releasebundle.ManifestDigest(staged.Release.Manifest) != digest {
		return staged, errors.New("cached release manifest differs from its digest")
	}
	return staged, nil
}

func (a *Agent) report(ctx context.Context, d model.ReleaseDesired, state, reason string, runtime *model.ReleaseRuntime) error {
	return a.reportComponent(ctx, d, state, reason, "", runtime)
}
func (a *Agent) reportComponent(ctx context.Context, d model.ReleaseDesired, state, reason, componentID string, runtime *model.ReleaseRuntime) error {
	a.sequence++
	report := model.ReleaseNodeReport{DeploymentID: d.DeploymentID, IdentityID: a.Workload.ID, NodeID: a.Workload.NodeID, Generation: d.Generation, Sequence: a.sequence, ReleaseSHA256: d.Release.SHA256, State: state, Runtime: runtime, Reason: reason}
	if state == "failed" {
		report.ComponentID = componentID
	}
	var target model.ReleaseTarget
	return a.call(ctx, a.Options.CloudURL, http.MethodPost, "/internal/releases/reports", report, &target)
}

func (a *Agent) preparationFailed(ctx context.Context, d model.ReleaseDesired, err error) error {
	var failed *PreparationError
	if errors.As(err, &failed) {
		if reportErr := a.reportComponent(ctx, d, "failed", err.Error(), failed.ComponentID, nil); reportErr != nil {
			return errors.Join(err, reportErr)
		}
	}
	return err
}

func (a *Agent) configurationReports(ctx context.Context, desired model.ReleaseDesired, run RuntimeResponse) error {
	refs := releasebundle.ReferencesForNode(desired.Release.Manifest, a.Options.NodeID)
	found := make(map[model.ConfigurationReference]bool, len(refs))
	for _, item := range run.Configurations {
		if !slices.Contains(refs, item.Reference) {
			continue
		}
		found[item.Reference] = true
		a.configurationSequence++
		version := model.ConfigurationVersion{Version: item.Reference.Version, Digest: item.Reference.Digest}
		report := model.ConfigurationReport{IdentityID: a.Workload.ID, NodeID: a.Workload.NodeID, Program: a.Workload.Program, Purpose: a.Workload.Purpose, InstanceID: a.InstanceID, InstanceEpoch: a.Workload.InstanceEpoch, Generation: a.Workload.Generation, Sequence: a.configurationSequence, Kind: item.Reference.Kind, ID: item.Reference.ID, Desired: version, Prepared: version, Applied: version, Running: version, State: "running", EffectiveValue: item.EffectiveValue}
		if e := a.call(ctx, a.Options.ConfigURL, http.MethodPost, "/internal/config/v2/reports", report, nil); e != nil {
			return e
		}
	}
	if len(found) != len(refs) {
		return errors.New("application runtime is missing a release configuration")
	}
	return nil
}

func (a *Agent) start(ctx context.Context, payloadPath, digest string) (RuntimeResponse, error) {
	var empty RuntimeResponse
	path, e := releasebundle.ArtifactPath(filepath.Join(a.Options.Directory, "artifacts"), digest)
	if e != nil {
		return empty, e
	}
	if _, e = releasebundle.VerifyArtifact(filepath.Join(a.Options.Directory, "artifacts"), digest); e != nil {
		return empty, e
	}
	args := []string{"-node-id", a.Options.NodeID, "-listen", a.Options.Listen, "-database", a.RuntimeOptions.Database, "-master-key", a.RuntimeOptions.MasterKeyFile, "-static", a.RuntimeOptions.StaticDirectory}
	if a.Options.Seed {
		args = append(args, "-seed")
	}
	if a.Options.DataTransferAddress != "" {
		args = append(args, "-datatransfer", a.Options.DataTransferAddress)
	}
	cmd := exec.Command(path, args...)
	environment := map[string]string{"PATH": os.Getenv("PATH"), "TMPDIR": os.TempDir(), "SF_BOOTSTRAP_PASSWORD": a.password}
	for name, value := range a.RuntimeOptions.Environment {
		environment[name] = value
	}
	environment["SF_SERVICE_TOKEN"] = a.localToken
	environment["SF_RELEASE_PAYLOAD"] = payloadPath
	for name, value := range environment {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	cmd.Dir = a.Options.Directory
	log, e := os.OpenFile(filepath.Join(a.Options.Directory, "program.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return empty, e
	}
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Start(); e != nil {
		log.Close()
		return empty, e
	}
	a.mu.Lock()
	a.child = cmd
	a.childDone = make(chan error, 1)
	done := a.childDone
	a.mu.Unlock()
	go func() { err := cmd.Wait(); log.Close(); done <- err; close(done) }()
	if a.Pending != nil && a.Pending.ProgramSHA256 == digest && a.Pending.PayloadPath == payloadPath {
		a.Pending.Runtime.PID = cmd.Process.Pid
		if e = WritePrivate(filepath.Join(a.Options.Directory, "pending.json"), a.Pending); e != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return empty, e
		}
	}
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case e := <-done:
			return empty, &applicationStartError{cause: fmt.Errorf("application exited before becoming ready: %v", e)}
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return empty, ctx.Err()
		case <-deadline.C:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return empty, &applicationStartError{cause: errors.New("application runtime readiness timed out")}
		case <-tick.C:
			run, e := a.Runtime(ctx)
			if e == nil && run.Runtime.PID == cmd.Process.Pid && run.Runtime.NodeID == a.Options.NodeID && run.Runtime.ProgramSHA256 == digest {
				return run, nil
			}
		}
	}
}

func (a *Agent) stop(ctx context.Context) error {
	a.mu.Lock()
	child, done := a.child, a.childDone
	a.mu.Unlock()
	run, probeErr := a.Runtime(ctx)
	var process *os.Process
	if probeErr == nil {
		if run.Runtime.NodeID != a.Options.NodeID || a.State.Runtime.ProcessInstanceID != "" && run.Runtime.ProcessInstanceID != a.State.Runtime.ProcessInstanceID {
			return errors.New("refusing to stop a process that differs from the verified local release")
		}
		var err error
		process, err = os.FindProcess(run.Runtime.PID)
		if err != nil {
			return err
		}
	} else if child != nil {
		select {
		case <-done:
			return nil
		default:
		}
		process = child.Process
	} else if a.State.Runtime.PID > 0 {
		if err := syscall.Kill(a.State.Runtime.PID, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return errors.New("persisted process is present but its runtime cannot be authenticated")
	} else {
		return nil
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if child != nil && child.Process.Pid == process.Pid {
			select {
			case <-done:
				return nil
			default:
			}
		} else if err := syscall.Kill(process.Pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("application process did not exit within ten seconds")
		case <-tick.C:
		}
	}
}

func (a *Agent) Restore(ctx context.Context) error {
	if a.State.ReleaseSHA256 == "" && a.Pending == nil {
		return nil
	}
	if a.State.ReleaseSHA256 != "" {
		staged, err := a.cachedRelease(a.State.ReleaseSHA256)
		if err != nil {
			return err
		}
		if a.State.PayloadPath != filepath.Join(a.Options.Directory, "releases", a.State.ReleaseSHA256+".json") || staged.Release.ID != a.State.ReleaseID || releasebundle.Program(staged.Release.Manifest).SHA256 != a.State.ProgramSHA256 {
			return errors.New("active release cache differs from its persisted identity")
		}
		if validation := releasebundle.Validate(ctx, staged.Release.Manifest); !validation.Valid {
			return errors.New("active release cache contains an invalid manifest")
		}
		refs := releasebundle.ReferencesForNode(staged.Release.Manifest, a.Options.NodeID)
		if len(refs) != len(staged.Configurations) {
			return errors.New("active release cache has an incomplete configuration set")
		}
		for index, envelope := range staged.Configurations {
			if envelope.Reference != refs[index] || envelope.NodeID != a.Options.NodeID || envelope.Program != staged.Release.Manifest.Program {
				return errors.New("active release cache configuration identity differs")
			}
		}
		if _, err = releasebundle.VerifyArtifact(filepath.Join(a.Options.Directory, "artifacts"), a.State.ProgramSHA256); err != nil {
			return err
		}
	}
	run, e := a.Runtime(ctx)
	if e != nil && a.Pending != nil && a.Pending.Runtime.PID > 0 && syscall.Kill(a.Pending.Runtime.PID, 0) == nil {
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for e != nil && syscall.Kill(a.Pending.Runtime.PID, 0) == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				return errors.New("pending application is present but has no authenticated runtime yet")
			case <-ticker.C:
				run, e = a.Runtime(ctx)
			}
		}
	}
	if e == nil {
		if a.Pending != nil && run.Runtime.ReleaseSHA256 == a.Pending.ReleaseSHA256 && run.Runtime.ProgramSHA256 == a.Pending.ProgramSHA256 && run.Runtime.NodeID == a.Options.NodeID && run.Runtime.PID == a.Pending.Runtime.PID {
			a.State = *a.Pending
			a.State.Runtime = run.Runtime
			return a.saveActive()
		}
		if run.Runtime.ReleaseSHA256 != a.State.ReleaseSHA256 || run.Runtime.ProgramSHA256 != a.State.ProgramSHA256 || run.Runtime.ProcessInstanceID != a.State.Runtime.ProcessInstanceID {
			return errors.New("existing process differs from the persisted active release")
		}
		return nil
	}
	if a.State.ReleaseSHA256 == "" {
		if a.Pending == nil {
			return nil
		}
		// The first release has no prior application. The verified staged target
		// is retained and resumed after online authority becomes available.
		return nil
	}
	if _, e := privateRead(a.State.PayloadPath); e != nil {
		return e
	}
	if a.State.Runtime.PID > 0 && syscall.Kill(a.State.Runtime.PID, 0) == nil {
		return errors.New("persisted application is present but its runtime cannot be authenticated")
	}
	if raw, err := privateRead(filepath.Join(a.Options.Directory, "restore-status.json")); err == nil {
		var status restoreStatus
		if err = store.DecodeJSON(raw, &status); err != nil {
			return err
		}
		if status.State == "awaiting_authorized_release" && status.Error != "" && status.DeploymentID == a.State.DeploymentID && status.Generation == a.State.Generation && status.ReleaseSHA256 == a.State.ReleaseSHA256 {
			a.restoreFailure = status.Error
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	run, e = a.start(ctx, a.State.PayloadPath, a.State.ProgramSHA256)
	if e != nil {
		return e
	}
	a.State.Runtime = run.Runtime
	return a.saveActive()
}

// PrepareCurrent supports the first service-manager handover while the
// original Edge still serves its fixed credential versions. It never starts
// or stops an application process and persists no claim of applied content.
func (a *Agent) PrepareCurrent(ctx context.Context) error {
	if a.State.ReleaseSHA256 != "" || a.Pending != nil && a.Pending.Runtime.PID > 0 {
		return errors.New("prepare-only is available before the first managed application starts")
	}
	if err := a.Activate(ctx); err != nil {
		return err
	}
	var desired model.ReleaseDesired
	if err := a.call(ctx, a.Options.CloudURL, http.MethodGet, "/internal/releases/desired", nil, &desired); err != nil {
		return err
	}
	if !desired.Available || desired.Release == nil {
		return errors.New("there is no active release assignment to prepare")
	}
	payload, err := a.prepare(ctx, desired)
	if err != nil {
		return a.preparationFailed(ctx, desired, err)
	}
	if err = a.report(ctx, desired, "prepared", "", nil); err != nil {
		return err
	}
	program := releasebundle.Program(desired.Release.Manifest)
	a.Pending = &State{AgentInstanceEpoch: a.Workload.InstanceEpoch, DeploymentID: desired.DeploymentID, Generation: desired.Generation, ReleaseID: desired.Release.ID, ReleaseSHA256: desired.Release.SHA256, ProgramSHA256: program.SHA256, PayloadPath: payload}
	return WritePrivate(filepath.Join(a.Options.Directory, "pending.json"), a.Pending)
}

func (a *Agent) Tick(ctx context.Context) error {
	if a.Workload.ID == "" {
		if e := a.Activate(ctx); e != nil {
			return e
		}
	}
	var desired model.ReleaseDesired
	if e := a.call(ctx, a.Options.CloudURL, http.MethodGet, "/internal/releases/desired", nil, &desired); e != nil {
		return e
	}
	if !desired.Available || desired.Release == nil {
		return nil
	}
	if a.restoreFailure != "" && desired.DeploymentID == a.State.DeploymentID && desired.Generation == a.State.Generation && desired.Release.SHA256 == a.State.ReleaseSHA256 {
		if !a.restoreFailureReported {
			if err := a.reportComponent(ctx, desired, "failed", a.restoreFailure, releasebundle.Program(desired.Release.Manifest).ID, nil); err != nil {
				return err
			}
			a.restoreFailureReported = true
		}
		return nil
	}
	run, probeErr := a.Runtime(ctx)
	if probeErr == nil && run.Runtime.ReleaseSHA256 == desired.Release.SHA256 && run.Runtime.ProgramSHA256 == releasebundle.Program(desired.Release.Manifest).SHA256 {
		if a.State.DeploymentID != desired.DeploymentID || a.State.Generation != desired.Generation || a.State.AgentInstanceEpoch != a.Workload.InstanceEpoch {
			if _, e := a.prepare(ctx, desired); e != nil {
				return a.preparationFailed(ctx, desired, e)
			}
		}
		if desired.Target == nil || desired.Target.PreparedSHA256 != desired.Release.SHA256 || desired.Target.AgentInstanceEpoch != a.Workload.InstanceEpoch {
			if e := a.report(ctx, desired, "prepared", a.restoreFailure, nil); e != nil {
				return e
			}
		}
		if e := a.configurationReports(ctx, desired, run); e != nil {
			return e
		}
		if e := a.report(ctx, desired, "running", "", &run.Runtime); e != nil {
			return e
		}
		a.State.DeploymentID = desired.DeploymentID
		a.State.Generation = desired.Generation
		a.State.AgentInstanceEpoch = a.Workload.InstanceEpoch
		a.State.Runtime = run.Runtime
		return a.saveActive()
	}
	payload, e := a.prepare(ctx, desired)
	if e != nil {
		return a.preparationFailed(ctx, desired, e)
	}
	if desired.Target == nil || desired.Target.PreparedSHA256 != desired.Release.SHA256 || desired.Target.AgentInstanceEpoch != a.Workload.InstanceEpoch {
		if e = a.report(ctx, desired, "prepared", a.restoreFailure, nil); e != nil {
			return e
		}
	}
	transition := Transition{DeploymentID: desired.DeploymentID, Generation: desired.Generation}
	if probeErr == nil {
		transition.OldPID = run.Runtime.PID
		transition.OldProcessInstanceID = run.Runtime.ProcessInstanceID
	}
	program := releasebundle.Program(desired.Release.Manifest)
	a.Pending = &State{AgentInstanceEpoch: a.Workload.InstanceEpoch, DeploymentID: desired.DeploymentID, Generation: desired.Generation, ReleaseID: desired.Release.ID, ReleaseSHA256: desired.Release.SHA256, ProgramSHA256: program.SHA256, PayloadPath: payload}
	if e = WritePrivate(filepath.Join(a.Options.Directory, "pending.json"), a.Pending); e != nil {
		return e
	}
	if e = a.stop(ctx); e != nil {
		_ = a.reportComponent(ctx, desired, "failed", "previous application did not stop: "+e.Error(), program.ID, nil)
		return e
	}
	transition.OldExitedMS = time.Now().UnixMilli()
	_ = WritePrivate(filepath.Join(a.Options.Directory, "last-transition.json"), transition)
	run, e = a.start(ctx, payload, program.SHA256)
	if e != nil {
		_ = a.reportComponent(ctx, desired, "failed", e.Error(), program.ID, nil)
		return e
	}
	transition.NewPID = run.Runtime.PID
	transition.NewProcessInstanceID = run.Runtime.ProcessInstanceID
	transition.NewReadyMS = time.Now().UnixMilli()
	if e = WritePrivate(filepath.Join(a.Options.Directory, "last-transition.json"), transition); e != nil {
		return e
	}
	a.State = State{AgentInstanceEpoch: a.Workload.InstanceEpoch, DeploymentID: desired.DeploymentID, Generation: desired.Generation, ReleaseID: desired.Release.ID, ReleaseSHA256: desired.Release.SHA256, ProgramSHA256: program.SHA256, PayloadPath: payload, Runtime: run.Runtime}
	if e = a.saveActive(); e != nil {
		return e
	}
	if e = a.configurationReports(ctx, desired, run); e != nil {
		return e
	}
	if e = a.report(ctx, desired, "applied", "", &run.Runtime); e != nil {
		return e
	}
	if err := a.report(ctx, desired, "running", "", &run.Runtime); err != nil {
		return err
	}
	a.restoreFailure = ""
	return nil
}

func (a *Agent) Run(ctx context.Context) error {
	if e := a.Restore(ctx); e != nil {
		var startup *applicationStartError
		if !errors.As(e, &startup) || ctx.Err() != nil {
			return e
		}
		if stopErr := a.stop(ctx); stopErr != nil {
			return errors.Join(e, stopErr)
		}
		a.restoreFailure = "cached release " + a.State.ReleaseSHA256 + " could not start: " + e.Error()
		if err := WritePrivate(filepath.Join(a.Options.Directory, "restore-status.json"), restoreStatus{InstanceID: a.InstanceID, AtMS: time.Now().UnixMilli(), DeploymentID: a.State.DeploymentID, Generation: a.State.Generation, ReleaseSHA256: a.State.ReleaseSHA256, Error: a.restoreFailure, State: "awaiting_authorized_release"}); err != nil {
			return err
		}
	}
	interval := a.Options.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		err := a.Tick(ctx)
		if err != nil && ctx.Err() == nil {
			_ = WritePrivate(filepath.Join(a.Options.Directory, "agent-status.json"), map[string]any{"instance_id": a.InstanceID, "at_ms": time.Now().UnixMilli(), "error": err.Error()})
		} else if err == nil {
			_ = WritePrivate(filepath.Join(a.Options.Directory, "agent-status.json"), map[string]any{"instance_id": a.InstanceID, "at_ms": time.Now().UnixMilli(), "state": "connected"})
		}
		select {
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return a.stop(stop)
		case <-ticker.C:
		}
	}
}

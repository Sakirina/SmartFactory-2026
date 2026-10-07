// sf-history-fixture prepares an isolated, runnable history-analysis workspace.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"competition2026/product/platform/internal/app"
	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	dir := flag.String("dir", "", "new isolated state directory (required)")
	address := flag.String("address", "127.0.0.1:18094", "loopback HTTP address")
	duration := flag.Duration("duration", 0, "stop after this duration; zero waits for SIGINT")
	static := flag.String("static", "", "optional frontend dist directory")
	flag.Parse()
	if *dir == "" {
		return errors.New("-dir is required and must not already exist")
	}
	host, _, err := net.SplitHostPort(*address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("fixture HTTP address must use a loopback IP")
	}
	if _, err = os.Stat(*dir); !errors.Is(err, os.ErrNotExist) {
		return errors.New("fixture directory already exists; choose a new directory")
	}
	if err = os.MkdirAll(*dir, 0700); err != nil {
		return err
	}
	secret := make([]byte, 24)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	password := hex.EncodeToString(secret)
	if err = os.WriteFile(filepath.Join(*dir, "private-credentials.json"), []byte(fmt.Sprintf("{\"login\":\"admin\",\"password\":%q}\n", password)), 0600); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := app.Open(ctx, app.Options{Mode: "cloud", NodeID: "history-fixture", Address: *address, DSN: filepath.Join(*dir, "history.db"), KeyFile: filepath.Join(*dir, "master.key"), BootstrapPassword: password, StaticDir: *static})
	if err != nil {
		return err
	}
	defer a.Close()
	a.Store.DB.SetMaxOpenConns(1)
	a.Store.DB.SetMaxIdleConns(1)
	_, principal, err := a.Server.Identity.Login(ctx, "admin", password, "", false, "history-fixture")
	if err != nil {
		return err
	}
	base := time.Now().UnixMilli() - 100000
	if err = prepare(ctx, a, principal, base); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	if err = driveLive(ctx, a); err != nil {
		stop()
		<-done
		return err
	}
	if err = saveSamples(ctx, a, principal, "http://"+*address, password, *dir); err != nil {
		stop()
		<-done
		return err
	}
	fmt.Printf("History fixture ready: http://%s; manifest=%s; private credentials=%s\n", *address, filepath.Join(*dir, "manifest.json"), filepath.Join(*dir, "private-credentials.json"))
	if *duration > 0 {
		timer := time.NewTimer(*duration)
		defer timer.Stop()
		select {
		case <-timer.C:
			stop()
		case <-ctx.Done():
		case err = <-done:
			return err
		}
	}
	return <-done
}

func prepare(ctx context.Context, a *app.Application, p identity.Principal, base int64) error {
	s := a.Store
	s.Now = func() time.Time { return time.UnixMilli(base) }
	for _, e := range []model.Entity{{ID: "history-factory", Kind: "asset", Name: "History fixture"}, {ID: "history-line-a", Kind: "asset", ParentID: "history-factory", Name: "Line A"}, {ID: "history-line-b", Kind: "asset", ParentID: "history-factory", Name: "Line B"}, {ID: "history-device", Kind: "device", ParentID: "history-line-a", Name: "History sensor", SamplingMS: 1000}} {
		if _, err := s.Put(ctx, "entity", e.ID, 0, e); err != nil {
			return err
		}
	}
	publish := func(d model.Definition) error {
		if _, err := s.Put(ctx, "definition", d.ID, d.Version-1, d); err != nil {
			return err
		}
		return a.Server.Engine.PrepareDefinition(ctx, d)
	}
	rule := func(id string, version int64, code, key string) model.Definition {
		return model.Definition{ID: id, Name: id, SchemaVersion: model.ContractVersion, Kind: "analysis", GroupID: "history-factory", Status: "published", Version: version, EffectiveMS: base + version*1000, Selector: model.Selector{DeviceIDs: []string{"history-device"}, Keys: []string{key}}, Nodes: []model.Node{{ID: "value", Type: "expression", Params: map[string]any{"code": code}}}, Outputs: []model.Output{{NodeID: "value", Key: "result", Type: "number", Unit: "piece"}}}
	}
	d := rule("history-assets", 1, "value+1", "count")
	d.Selector.AssetID = "history-line-a"
	if err := publish(d); err != nil {
		return err
	}
	d.Version, d.EffectiveMS, d.Selector.AssetID = 2, base+3000, "history-line-b"
	d.Nodes[0].Params = map[string]any{"code": "value+2"}
	if err := publish(d); err != nil {
		return err
	}
	s.Now = func() time.Time { return time.UnixMilli(base + 3000) }
	if _, err := s.Put(ctx, "entity", "history-device", 1, model.Entity{ID: "history-device", Kind: "device", ParentID: "history-line-b", Name: "History sensor", SamplingMS: 1000}); err != nil {
		return err
	}
	s.Now = time.Now
	d = rule("history-window", 1, "value", "count")
	d.Selector.WindowMS = 1000
	d.Nodes = append([]model.Node{{ID: "sum", Type: "aggregate", Params: map[string]any{"function": "sum"}}}, d.Nodes...)
	d.Connections = []model.Connection{{From: "sum", To: "value"}}
	for version := int64(1); version <= 3; version++ {
		d.Version, d.EffectiveMS = version, base+version*1000
		if version > 1 {
			d.Nodes[1].Params = map[string]any{"code": "value+1"}
		}
		if err := publish(d); err != nil {
			return err
		}
	}
	point := func(id string, offset int64, value any) model.Observation {
		return model.Observation{ID: id, MessageID: id, SourceID: "history-fixture-source", SourceSequence: uint64(offset), DeviceID: "history-device", Key: "count", Value: value, ObservedMS: base + offset, ReceivedMS: base + 100000, TimeSource: "device", Quality: "GOOD", Unit: "piece", Revision: 1, AssetVersion: 1}
	}
	first, second := point("asset-before", 2000, json.Number("9007199254740993")), point("asset-after", 3500, 4)
	second.Late, second.Quality, second.QualityReason, second.AssetVersion = true, "UNCERTAIN", "source sent after affiliation changed", 2
	history := a.Server.HistoryApplication()
	requests := []historymodel.Request{{ID: "fixture-replay-assets", Kind: "replay", DefinitionID: "history-assets", FromMS: base + 1500, ToMS: base + 4000, Points: []model.Observation{second, first}, History: []model.Observation{}}}
	at, freshness := base+9000, int64(5000)
	comparison := historymodel.Request{ID: "fixture-compare-changed", Kind: "compare", DefinitionID: "history-window", LeftVersion: 1, RightVersion: 2, FromMS: base + 3000, ToMS: base + 4000, Points: []model.Observation{point("window-third", 4000, 5), point("window-first", 3000, json.Number("9007199254740993")), point("window-second", 3500, 2)}, History: []model.Observation{point("window-history", 2501, 3)}, InitialState: historymodel.State{}, Clock: historymodel.Clock{AtMS: &at, StepMS: 10, FreshnessMS: &freshness}}
	requests = append(requests, comparison)
	comparison.ID, comparison.LeftVersion, comparison.RightVersion = "fixture-compare-unchanged", 2, 3
	requests = append(requests, comparison)
	for _, request := range requests {
		run, err := history.Create(ctx, p, request)
		if err != nil {
			return err
		}
		if err = history.Execute(ctx, run.ID); err != nil {
			return err
		}
	}
	cancelled := requests[0]
	cancelled.ID = "fixture-cancelled"
	run, err := history.Create(ctx, p, cancelled)
	if err != nil {
		return err
	}
	task, err := s.Task(ctx, run.TaskID)
	if err != nil {
		return err
	}
	if _, err = a.Server.TaskApplication().Change(ctx, p, task.ID, task.Version, "cancel"); err != nil {
		return err
	}
	failed := rule("history-failed", 1, "value", "failure")
	failed.Nodes = []model.Node{{ID: "value", Type: "input", Params: map[string]any{"key": "missing_field"}}}
	if err = publish(failed); err != nil {
		return err
	}
	failed.Version, failed.EffectiveMS = 2, base+2000
	failed.Nodes = []model.Node{{ID: "value", Type: "expression", Params: map[string]any{"code": "value"}}}
	if err = publish(failed); err != nil {
		return err
	}
	upstream := rule("history-source", 1, "value", "live")
	upstream.Selector.WindowMS = 60000
	upstream.Nodes = []model.Node{{ID: "value", Type: "aggregate", Params: map[string]any{"function": "sum"}}}
	if err = publish(upstream); err != nil {
		return err
	}
	for version := int64(1); version <= 2; version++ {
		code := "value+1"
		if version == 2 {
			code = "value"
		}
		downstream := rule("history-derived", version, code, "history-source.result")
		downstream.Dependencies = []string{upstream.ID}
		if err = publish(downstream); err != nil {
			return err
		}
	}
	for _, candidate := range []historymodel.ShadowRequest{{ID: "fixture-live", DefinitionID: "history-derived", Version: 1}, {ID: "fixture-failure", DefinitionID: "history-failed", Version: 1}} {
		if _, err = history.EnableShadow(ctx, p, candidate); err != nil {
			return err
		}
	}
	return nil
}

func driveLive(ctx context.Context, a *app.Application) error {
	s := a.Store
	base := time.Now().UnixMilli() - 3000
	input := func(id, key string, offset int64, value int) error {
		_, err := s.Ingest(ctx, store.IngestBatch{MessageID: id, SourceID: "fixture-live-source", Points: []model.Observation{{ID: id, MessageID: id, SourceID: "fixture-live-source", SourceSequence: uint64(base + offset), DeviceID: "history-device", Key: key, Value: value, ObservedMS: base + offset, ReceivedMS: time.Now().UnixMilli(), TimeSource: "device", Quality: "GOOD", Revision: 1}}})
		return err
	}
	wait := func(id string, condition func(historymodel.Run) bool) error {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			run, err := s.AnalysisRun(ctx, id)
			if err == nil && condition(run) {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(30 * time.Millisecond):
			}
		}
		run, _ := s.AnalysisRun(ctx, id)
		return fmt.Errorf("fixture run %s did not reach expected state: %s %s", id, run.Status, run.Error)
	}
	complete := func(r historymodel.Run) bool { return r.Status == "completed" }
	if err := input("fixture-live-first", "live", 0, 10); err != nil {
		return err
	}
	if err := wait("shadow:fixture-live:1:1", complete); err != nil {
		return err
	}
	if err := input("fixture-live-second", "live", 2000, 30); err != nil {
		return err
	}
	if err := wait("shadow:fixture-live:1:2", complete); err != nil {
		return err
	}
	if err := input("fixture-live-late", "live", 1000, 20); err != nil {
		return err
	}
	if err := wait("shadow:fixture-live:1:4", complete); err != nil {
		return err
	}
	for index := 0; index < 2; index++ {
		if err := input(fmt.Sprintf("fixture-failure-%d", index), "failure", 2100+int64(index), 5); err != nil {
			return err
		}
	}
	if err := wait("shadow:fixture-failure:1:1", func(r historymodel.Run) bool { return r.Status == "failed" }); err != nil {
		return err
	}
	return wait("shadow:fixture-failure:1:2", func(r historymodel.Run) bool { return r.Status == "waiting_parent" })
}

func saveSamples(ctx context.Context, a *app.Application, p identity.Principal, baseURL, password, dir string) error {
	token, _, err := a.Server.Identity.Login(ctx, "admin", password, "", false, "history-fixture-http")
	if err != nil {
		return err
	}
	paths := []string{"/analysis-runs", "/shadow-candidates", "/tasks"}
	runs, err := a.Server.HistoryApplication().List(ctx, p, "", 500)
	if err != nil {
		return err
	}
	for _, run := range runs.Items {
		for _, suffix := range []string{"", "/snapshot", "/steps"} {
			paths = append(paths, "/analysis-runs/"+run.ID+suffix)
		}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	samples := map[string]json.RawMessage{}
	for _, path := range paths {
		req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/api/sf/v1"+path, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 {
			return fmt.Errorf("fixture sample %s status=%d: %s: %w", path, res.StatusCode, raw, err)
		}
		samples[path] = raw
	}
	manifest := map[string]any{"base_url": baseURL, "database": "history.db", "samples": "http-samples.json", "generated_ms": time.Now().UnixMilli(), "uses_production_workers": true, "runs": runs.Items, "expected": map[string]any{"cross_version_exact_value": "9007199254740994", "window_left_values": []string{"9007199254740996", "9007199254740998", "7"}, "unchanged_comparison": false, "derived_revision": 2, "shadow_candidate_value": "61", "formal_value": "60", "failed_run": "shadow:fixture-failure:1:1", "waiting_run": "shadow:fixture-failure:1:2", "cancelled_run": "fixture-cancelled"}}
	for name, value := range map[string]any{"http-samples.json": samples, "manifest.json": manifest} {
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, name), append(raw, '\n'), 0600); err != nil {
			return err
		}
	}
	// Validate the actual generated response before announcing readiness.
	var steps historymodel.StepList
	if err = store.DecodeJSON(samples["/analysis-runs/shadow:fixture-live:1:4/steps"], &steps); err != nil || len(steps.Items) != 3 {
		return fmt.Errorf("fixture revised steps unavailable: %w", err)
	}
	last := steps.Items[2]
	if last.Point.Revision != 2 || last.FormalEvaluation == nil || fmt.Sprint(last.Lanes[0].Evaluation.Values["value"]) != "61" || fmt.Sprint(last.FormalEvaluation.Values["value"]) != "60" || !strings.Contains(last.FormalStatus, "recompute") {
		return errors.New("fixture formal revision comparison does not match expected values")
	}
	return nil
}

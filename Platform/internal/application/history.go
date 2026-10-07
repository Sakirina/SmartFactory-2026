package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/historymodel"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/observability"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type HistoryRepository interface {
	DocumentQueries
	CurrentTime() time.Time
	Versions(context.Context, string, string) ([]store.Document, error)
	Query(context.Context, store.Query) (model.DataResult, error)
	CreateAnalysis(context.Context, historymodel.Run, historymodel.Snapshot, model.Actor, func(*store.Tx) error) (historymodel.Run, error)
	AnalysisRun(context.Context, string) (historymodel.Run, error)
	AnalysisSnapshot(context.Context, string) (historymodel.Snapshot, error)
	AnalysisSteps(context.Context, string, int, int) (historymodel.StepList, error)
	BeginAnalysis(context.Context, string) (historymodel.Run, error)
	CommitAnalysisStep(context.Context, historymodel.Run, historymodel.Step, map[string]historymodel.State) (historymodel.Run, error)
	FailAnalysis(context.Context, string, error) error
	SaveShadow(context.Context, historymodel.Candidate, int64, model.Actor, func(*store.Tx) error) (historymodel.Candidate, error)
	StopShadow(context.Context, string, int64, model.Actor, func(*store.Tx) error) (historymodel.Candidate, error)
	ShadowInputs(context.Context, string, int64) ([]model.Observation, error)
	ShadowInputKnown(context.Context, string, int64, model.Observation) (bool, error)
	CaptureShadow(context.Context, historymodel.Candidate, model.Observation, historymodel.Run, historymodel.Snapshot) error
	PauseShadow(context.Context, string, int64, string) error
}

type HistoryPlans interface {
	PublishedPlan(context.Context, model.Definition) (*model.ExecutionPlan, error)
}
type History struct {
	Store    HistoryRepository
	Identity *identity.Manager
	Engine   HistoryPlans
}

func (s *History) authorize(ctx context.Context, p identity.Principal, resources []string, action string) (*revisions, error) {
	guard := newRevisions()
	access := &authorizationReader{Store: s.Store, Identity: s.Identity}
	if err := access.captureAuthorization(ctx, p, guard); err != nil {
		return nil, err
	}
	if err := access.permit(ctx, p, action, "", guard); err != nil {
		return nil, err
	}
	for _, resource := range resources {
		if resource == "" {
			return nil, identity.ErrDenied
		}
		if err := access.permit(ctx, p, "read", resource, guard); err != nil {
			return nil, err
		}
	}
	return guard, nil
}

func historyResources(rules []historymodel.Rule, points, history []model.Observation, assets []model.AssetVersion, initial historymodel.State) []string {
	all := []string{}
	for _, rule := range rules {
		d := rule.Definition
		all = append(all, d.GroupID)
		all = append(all, d.Selector.DeviceIDs...)
		if d.Selector.AssetID != "" {
			all = append(all, d.Selector.AssetID)
		}
		for _, c := range d.Policy.Conditions {
			all = append(all, c.DeviceID)
		}
		for _, step := range append(append([]model.Step{}, d.Policy.Steps...), d.Policy.Degraded...) {
			all = append(all, step.DeviceID)
		}
	}
	for _, p := range append(append([]model.Observation{}, points...), history...) {
		all = append(all, p.DeviceID)
	}
	for _, a := range assets {
		all = append(all, a.ID)
		if a.ParentID != "" {
			all = append(all, a.ParentID)
		}
	}
	for id := range initial {
		all = append(all, id)
	}
	return uniqueHistory(all)
}
func uniqueHistory(items []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
func includesHistory(items []string, id string) bool {
	for _, s := range items {
		if s == id {
			return true
		}
	}
	return false
}

func (s *History) rules(ctx context.Context, request historymodel.Request) ([]historymodel.Rule, error) {
	docs, err := s.Store.Versions(ctx, "definition", request.DefinitionID)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, store.ErrNotFound
	}
	rules := []historymodel.Rule{}
	var beforeVersion int64
	if request.Kind != "compare" {
		for _, doc := range docs {
			d, err := store.Decode[model.Definition](doc)
			if err != nil {
				return nil, err
			}
			if d.EffectiveMS <= request.FromMS {
				beforeVersion = max(beforeVersion, d.Version)
			}
		}
	}
	for _, doc := range docs {
		d, err := store.Decode[model.Definition](doc)
		if err != nil {
			return nil, err
		}
		if request.Kind == "compare" {
			if d.Version != request.LeftVersion && d.Version != request.RightVersion {
				continue
			}
			if d.Status != "published" {
				return nil, errors.New("comparison requires two published definition versions")
			}
		} else if d.EffectiveMS > request.ToMS || d.EffectiveMS <= request.FromMS && d.Version != beforeVersion {
			continue
		}
		r := historymodel.Rule{Definition: d}
		if d.Status == "published" {
			r.Plan, err = s.Engine.PublishedPlan(ctx, d)
			if err != nil {
				return nil, fmt.Errorf("prepare definition %s version %d before analysis: %w", d.ID, d.Version, err)
			}
		}
		rules = append(rules, r)
		if len(rules) > 256 {
			return nil, errors.New("analysis exceeds 256 definition versions; reduce the interval")
		}
	}
	if len(rules) == 0 {
		return nil, errors.New("no definition version is available in the requested interval")
	}
	if request.Kind == "compare" {
		found := map[int64]bool{}
		for _, r := range rules {
			found[r.Definition.Version] = true
		}
		if !found[request.LeftVersion] || !found[request.RightVersion] {
			return nil, store.ErrNotFound
		}
	}
	return rules, nil
}

func (s *History) dependencyResources(ctx context.Context, rules []historymodel.Rule) ([]string, error) {
	resources := []string{}
	seen := map[string]bool{}
	var visit func(model.Definition) error
	visit = func(d model.Definition) error {
		key := fmt.Sprintf("%s:%d", d.ID, d.Version)
		if seen[key] {
			return nil
		}
		seen[key] = true
		if len(seen) > 256 {
			return errors.New("analysis dependency snapshot exceeds 256 definitions")
		}
		resources = append(resources, historyResources([]historymodel.Rule{{Definition: d}}, nil, nil, nil, nil)...)
		for _, id := range d.Dependencies {
			docs, err := s.Store.Versions(ctx, "definition", id)
			if err != nil {
				return err
			}
			var found *model.Definition
			for _, doc := range docs {
				dep, err := store.Decode[model.Definition](doc)
				if err != nil {
					return err
				}
				if dep.EffectiveMS <= d.EffectiveMS {
					if found == nil || dep.Version > found.Version {
						copy := dep
						found = &copy
					}
				}
			}
			if found == nil {
				return fmt.Errorf("historical dependency %s unavailable", id)
			}
			if err = visit(*found); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range rules {
		if err := visit(r.Definition); err != nil {
			return nil, err
		}
	}
	return uniqueHistory(resources), nil
}

func (s *History) assets(ctx context.Context, points []model.Observation, to int64) ([]model.AssetVersion, error) {
	pending := []string{}
	for _, p := range points {
		pending = append(pending, p.DeviceID)
	}
	seen := map[string]bool{}
	result := []model.AssetVersion{}
	for len(pending) > 0 {
		id := pending[0]
		pending = pending[1:]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if len(seen) > historymodel.MaxHistory {
			return nil, errors.New("asset hierarchy exceeds snapshot budget")
		}
		docs, err := s.Store.Versions(ctx, "entity", id)
		if err != nil {
			return nil, err
		}
		for _, doc := range docs {
			if doc.UpdatedMS > to {
				continue
			}
			entity, err := store.Decode[model.Entity](doc)
			if err != nil {
				return nil, err
			}
			result = append(result, model.AssetVersion{ID: id, Version: doc.Version, EffectiveMS: doc.UpdatedMS, ParentID: entity.ParentID, SamplingMS: entity.SamplingMS})
			pending = append(pending, entity.ParentID)
		}
	}
	return result, nil
}

func (s *History) prepare(ctx context.Context, request historymodel.Request) (historymodel.Snapshot, error) {
	snapshot := historymodel.Snapshot{FromMS: request.FromMS, ToMS: request.ToMS, Order: request.Order, Clock: request.Clock, InitialState: request.InitialState, CapturedMS: s.Store.CurrentTime().UnixMilli(), Source: "provided"}
	if request.Kind != "replay" && request.Kind != "compare" {
		return snapshot, errors.New("kind must be replay or compare")
	}
	if request.FromMS <= 0 || request.ToMS < request.FromMS {
		return snapshot, errors.New("a positive from_ms and to_ms >= from_ms are required")
	}
	if request.Kind == "compare" && (request.LeftVersion < 1 || request.RightVersion < 1) {
		return snapshot, errors.New("left_version and right_version must identify published versions")
	}
	var err error
	snapshot.Rules, err = s.rules(ctx, request)
	if err != nil {
		return snapshot, err
	}
	devices := append([]string{}, request.DeviceIDs...)
	keys := append([]string{}, request.Keys...)
	var window int64
	inputKeys := []string{}
	for _, r := range snapshot.Rules {
		window = max(window, r.Definition.Selector.WindowMS)
		if len(request.DeviceIDs) == 0 {
			devices = append(devices, r.Definition.Selector.DeviceIDs...)
		}
		if len(request.Keys) == 0 {
			keys = append(keys, r.Definition.Selector.Keys...)
		}
		if r.Plan != nil {
			for _, n := range r.Plan.Nodes {
				if n.Type == "input" && n.Params.Key != "" {
					inputKeys = append(inputKeys, n.Params.Key)
				}
			}
		}
	}
	devices = uniqueHistory(devices)
	keys = uniqueHistory(keys)
	inputKeys = uniqueHistory(inputKeys)
	snapshot.Points = append([]model.Observation{}, request.Points...)
	if request.Points == nil {
		if len(devices) == 0 {
			return snapshot, errors.New("device_ids is required for an asset-scoped database analysis")
		}
		data, err := s.Store.Query(ctx, store.Query{DeviceIDs: devices, Keys: keys, FromMS: request.FromMS, ToMS: request.ToMS, Limit: historymodel.MaxPoints})
		if err != nil {
			return snapshot, err
		}
		if data.Truncated {
			return snapshot, errors.New("input budget exceeded; reduce interval or device scope")
		}
		snapshot.Points = data.Points
		snapshot.Source = "database_snapshot"
		snapshot.DataVersion = data.DataVersion
		snapshot.DataGaps = data.Gaps
		snapshot.Completeness = data.Quality.Completeness
	}
	if len(snapshot.Points) == 0 {
		return snapshot, errors.New("no retained inputs in requested range; select a range containing available raw observations")
	}
	for _, p := range snapshot.Points {
		devices = append(devices, p.DeviceID)
	}
	devices = uniqueHistory(devices)
	snapshot.History = append([]model.Observation{}, request.History...)
	if request.History == nil && (window > 0 || len(inputKeys) > 0) {
		from := max(int64(1), request.FromMS-max(int64(1), window)+1)
		historyKeys := uniqueHistory(append(append([]string{}, keys...), inputKeys...))
		data, err := s.Store.Query(ctx, store.Query{DeviceIDs: devices, Keys: historyKeys, FromMS: from, ToMS: request.ToMS, Limit: historymodel.MaxHistory})
		if err != nil {
			return snapshot, err
		}
		if data.Truncated {
			return snapshot, errors.New("history budget exceeded; reduce the requested interval")
		}
		snapshot.History = data.Points
		for _, device := range devices {
			for _, key := range inputKeys {
				data, err := s.Store.Query(ctx, store.Query{DeviceIDs: []string{device}, Keys: []string{key}, FromMS: 0, ToMS: from - 1, Limit: 1})
				if err != nil {
					return snapshot, err
				}
				snapshot.History = append(snapshot.History, data.Points...)
			}
		}
	}
	snapshot.AssetVersions = append([]model.AssetVersion{}, request.AssetVersions...)
	if request.AssetVersions == nil {
		snapshot.AssetVersions, err = s.assets(ctx, append(append([]model.Observation{}, snapshot.Points...), snapshot.History...), request.ToMS)
		if err != nil {
			return snapshot, err
		}
	}
	if err = engine.NormalizeAnalysis(&snapshot); err != nil {
		return snapshot, err
	}
	resources, err := s.dependencyResources(ctx, snapshot.Rules)
	if err != nil {
		return snapshot, err
	}
	snapshot.Resources = uniqueHistory(append(resources, historyResources(snapshot.Rules, snapshot.Points, snapshot.History, snapshot.AssetVersions, snapshot.InitialState)...))
	return finalizeHistorySnapshot(snapshot)
}

func finalizeHistorySnapshot(snapshot historymodel.Snapshot) (historymodel.Snapshot, error) {
	sum, err := historymodel.SnapshotDigest(snapshot)
	snapshot.SHA256 = sum
	snapshot.ID = "snapshot:" + sum
	return snapshot, err
}
func newHistoryRun(id, kind, definition string, snapshot historymodel.Snapshot) historymodel.Run {
	return historymodel.Run{ID: id, Kind: kind, DefinitionID: definition, SnapshotID: snapshot.ID, CapturedSnapshotID: snapshot.ID, SnapshotSHA256: snapshot.SHA256, InputSHA256: snapshot.InputSHA256, TaskID: "analysis:" + id, Status: "pending", Total: len(snapshot.Points), FinalState: map[string]historymodel.State{}, Resources: snapshot.Resources, CreatedMS: snapshot.CapturedMS, UpdatedMS: snapshot.CapturedMS, Version: 1, InitialStateResolved: snapshot.InitialStateRunID == ""}
}

func (s *History) Create(ctx context.Context, p identity.Principal, request historymodel.Request) (result historymodel.Run, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ctx, finish := observability.StartOperation(ctx, "analysis.create", observability.Identity{DefinitionID: request.DefinitionID})
	defer func() { finish(err) }()
	snapshot, err := s.prepare(ctx, request)
	if err != nil {
		return historymodel.Run{}, err
	}
	guard, err := s.authorize(ctx, p, snapshot.Resources, "draft")
	if err != nil {
		return historymodel.Run{}, err
	}
	if request.ID == "" {
		request.ID = "analysis:" + store.Hash([]any{snapshot.SHA256, s.Store.CurrentTime().UnixNano(), p.Actor.UserID})[:24]
	}
	run := newHistoryRun(request.ID, request.Kind, request.DefinitionID, snapshot)
	run.LeftVersion, run.RightVersion = request.LeftVersion, request.RightVersion
	return s.Store.CreateAnalysis(ctx, run, snapshot, p.Actor, guard.check)
}

func (s *History) Get(ctx context.Context, p identity.Principal, id string) (historymodel.Run, error) {
	run, err := s.Store.AnalysisRun(ctx, id)
	if err != nil {
		return run, err
	}
	if _, err = s.authorize(ctx, p, run.Resources, "read"); err != nil {
		return historymodel.Run{}, err
	}
	return run, nil
}
func (s *History) Snapshot(ctx context.Context, p identity.Principal, id string) (historymodel.Snapshot, error) {
	run, err := s.Get(ctx, p, id)
	if err != nil {
		return historymodel.Snapshot{}, err
	}
	return s.Store.AnalysisSnapshot(ctx, run.SnapshotID)
}
func (s *History) Steps(ctx context.Context, p identity.Principal, id string, after, limit int) (historymodel.StepList, error) {
	if _, err := s.Get(ctx, p, id); err != nil {
		return historymodel.StepList{}, err
	}
	result, err := s.Store.AnalysisSteps(ctx, id, after, limit)
	if err != nil {
		return result, err
	}
	for _, step := range result.Items {
		if step.FormalEvaluation == nil {
			continue
		}
		versions, err := s.Store.Versions(ctx, "definition", step.FormalEvaluation.DefinitionID)
		if err != nil {
			return historymodel.StepList{}, err
		}
		for _, version := range versions {
			d, err := store.Decode[model.Definition](version)
			if err != nil {
				return historymodel.StepList{}, err
			}
			if d.Version != step.FormalEvaluation.Version {
				continue
			}
			resources, err := s.dependencyResources(ctx, []historymodel.Rule{{Definition: d}})
			if err != nil {
				return historymodel.StepList{}, err
			}
			resources = uniqueHistory(append(resources, historyResources([]historymodel.Rule{{Definition: d}}, step.FormalOutputs, nil, step.FormalEvaluation.AssetVersions, nil)...))
			if _, err = s.authorize(ctx, p, resources, "read"); err != nil {
				return historymodel.StepList{}, err
			}
			break
		}
	}
	return result, nil
}
func (s *History) List(ctx context.Context, p identity.Principal, after string, limit int) (historymodel.RunList, error) {
	out := historymodel.RunList{Items: []historymodel.Run{}}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	docs, err := s.Store.List(ctx, "analysis_run")
	if err != nil {
		return out, err
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
	scanned := 0
	for _, doc := range docs {
		if doc.ID <= after {
			continue
		}
		scanned++
		out.Next = doc.ID
		run, err := s.Get(ctx, p, doc.ID)
		if err != nil && !errors.Is(err, identity.ErrDenied) {
			return out, err
		}
		if err == nil {
			out.Items = append(out.Items, run)
		}
		if scanned == limit {
			break
		}
	}
	if scanned < limit {
		out.Next = ""
	}
	return out, nil
}

func (s *History) Execute(ctx context.Context, id string) (err error) {
	ctx, finish := observability.StartOperation(ctx, "analysis.run", observability.Identity{MessageID: id})
	defer func() {
		if err != nil && !errors.Is(err, historymodel.ErrParentPending) {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			err = errors.Join(err, s.Store.FailAnalysis(saveCtx, id, err))
			cancel()
		}
		finish(err)
	}()
	run, err := s.Store.BeginAnalysis(ctx, id)
	if err != nil {
		return err
	}
	if run.Status == "completed" {
		return nil
	}
	snapshot, err := s.Store.AnalysisSnapshot(ctx, run.SnapshotID)
	if err != nil {
		return err
	}
	if snapshot.SHA256 != run.SnapshotSHA256 {
		return errors.New("run and input snapshot digests differ")
	}
	for run.Cursor < run.Total {
		if err = ctx.Err(); err != nil {
			return err
		}
		stepCtx, done := observability.StartOperation(ctx, "analysis.evaluate", observability.Identity{MessageID: snapshot.Points[run.Cursor].MessageID, DefinitionID: run.DefinitionID})
		trace.SpanFromContext(stepCtx).SetAttributes(attribute.String("smartfactory.run_id", run.ID), attribute.String("smartfactory.snapshot_id", snapshot.ID))
		step, states, e := engine.AnalysisStep(stepCtx, run, snapshot, run.Cursor)
		if e == nil {
			plans, sides := []string{}, []string{}
			versions := []int64{}
			for _, lane := range step.Lanes {
				plans = append(plans, lane.PlanID)
				versions = append(versions, lane.DefinitionVersion)
				sides = append(sides, lane.Side)
			}
			trace.SpanFromContext(stepCtx).SetAttributes(attribute.StringSlice("smartfactory.plan_ids", plans), attribute.Int64Slice("smartfactory.definition_versions", versions), attribute.StringSlice("smartfactory.analysis_sides", sides))
			run, e = s.Store.CommitAnalysisStep(stepCtx, run, step, states)
		}
		done(e)
		if e != nil {
			return e
		}
	}
	return nil
}

var _ HistoryRepository = (*store.Store)(nil)

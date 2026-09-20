package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Service struct {
	Store          *store.Store
	ControlEnabled bool
	mu             sync.Mutex
	strategyMu     sync.Mutex
}

var nodeKinds = model.NodeKinds()

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
func (s *Service) Validate(ctx context.Context, d model.Definition) model.Validation {
	v := model.Validation{Valid: true, Errors: []string{}, Order: []string{}}
	add := func(msg string) { v.Valid = false; v.Errors = append(v.Errors, msg) }
	if d.ID == "" || d.Name == "" {
		add("id and name are required")
	}
	if !has([]string{"analysis", "alarm", "strategy"}, d.Kind) {
		add("kind must be analysis, alarm or strategy")
	}
	if d.SchemaVersion != model.ContractVersion {
		add("unsupported schema_version")
	}
	if d.GroupID == "" {
		add("group_id is required")
	}
	if len(d.Nodes) == 0 || len(d.Nodes) > 128 {
		add("definition must have 1 to 128 nodes")
	}
	if len(d.Connections) > 512 {
		add("definition exceeds 512 connections")
	}
	nodes := map[string]model.Node{}
	incoming := map[string]int{}
	outgoing := map[string][]string{}
	for _, n := range d.Nodes {
		if n.ID == "" {
			add("node id is required")
		}
		if _, ok := nodes[n.ID]; ok {
			add("duplicate node " + n.ID)
		}
		nodes[n.ID] = n
		if !has(nodeKinds[n.Type], d.Kind) {
			add("node " + n.ID + " is not allowed in " + d.Kind)
		}
		if n.Type == "expression" {
			code, _ := n.Params["code"].(string)
			if code == "" {
				add("expression code is required")
			}
			var expressionError error
			for _, value := range []any{1, false, "sample"} {
				_, expressionError = Expression(ctx, code, map[string]any{"value": value, "sum": 1, "count": 1, "min": 1, "max": 1, "avg": 1, "previous": value, "active": false, "good": true, "quality": "GOOD", "fresh": true})
				if expressionError == nil || strings.Contains(expressionError.Error(), "division by zero") {
					expressionError = nil
					break
				}
			}
			if e := expressionError; e != nil {
				add("expression " + n.ID + ": " + e.Error())
			}
		}
		if n.Type == "aggregate" {
			fn, _ := n.Params["function"].(string)
			if !has([]string{"max", "min", "sum", "count", "avg"}, fn) {
				add("unsupported aggregate function")
			}
		}
		if n.Type == "debounce" {
			mode, _ := n.Params["mode"].(string)
			if mode != "" && mode != "both" && mode != "activation" {
				add("debounce mode must be both or activation")
			}
			delay := integer(n.Params["duration_ms"], 0)
			if delay < 1 || delay > 86400000 {
				add("debounce duration_ms must be 1..86400000")
			}
		}
	}
	for _, c := range d.Connections {
		from, ok1 := nodes[c.From]
		to, ok2 := nodes[c.To]
		if !ok1 || !ok2 {
			add("connection references a missing node")
			continue
		}
		ft, tt := "any", "any"
		foundF, foundT := len(from.Outputs) == 0, len(to.Inputs) == 0
		for _, p := range from.Outputs {
			if p.Name == c.FromPort {
				ft = p.Type
				foundF = true
			}
		}
		for _, p := range to.Inputs {
			if p.Name == c.ToPort {
				tt = p.Type
				foundT = true
			}
		}
		if !foundF || !foundT {
			add("connection references a missing port")
		}
		if ft != "any" && tt != "any" && ft != tt {
			add("port type mismatch " + c.From + " -> " + c.To)
		}
		incoming[c.To]++
		outgoing[c.From] = append(outgoing[c.From], c.To)
	}
	ready := []string{}
	for id := range nodes {
		if incoming[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		v.Order = append(v.Order, id)
		for _, next := range outgoing[id] {
			incoming[next]--
			if incoming[next] == 0 {
				ready = append(ready, next)
				sort.Strings(ready)
			}
		}
	}
	if len(v.Order) != len(nodes) {
		add("graph contains a cycle")
	}
	for _, o := range d.Outputs {
		if _, ok := nodes[o.NodeID]; !ok {
			add("output references a missing node")
		}
		if o.Key == "" {
			add("output key is required")
		}
	}
	for _, dependency := range d.Dependencies {
		if dependency == d.ID {
			add("definition cannot depend on itself")
			continue
		}
		at, _ := ctx.Value(historicalAtKey{}).(int64)
		other, e := s.Published(ctx, dependency, at)
		if e != nil || other.Status != "published" {
			add("dependency is unavailable: " + dependency)
		} else if has(other.Dependencies, d.ID) {
			add("dependency cycle: " + dependency)
		}
	}
	if d.Kind == "strategy" {
		if d.Policy.FreshnessMS < 0 || d.Policy.FreshnessMS > 86400000 {
			add("freshness_ms must be 0..86400000")
		}
		if d.Policy.Watchdog && (len(d.Selector.DeviceIDs) == 0 || len(d.Selector.Keys) == 0) {
			add("watchdog requires explicit device and field selectors")
		}
		if len(d.Policy.Steps) == 0 {
			add("strategy requires at least one device step")
		}
		if len(d.Policy.EdgeIDs) > 1 && len(d.Policy.Degraded) == 0 {
			add("cross-edge strategy requires degraded steps")
		}
		steps := map[string]bool{}
		for _, step := range append(append([]model.Step{}, d.Policy.Steps...), d.Policy.Degraded...) {
			if step.ID == "" || step.DeviceID == "" || step.Action == "" || step.EdgeID == "" {
				add("step requires id, edge_id, device_id and action")
			}
			if steps[step.ID] {
				add("step IDs must be unique")
			}
			steps[step.ID] = true
		}
		if sc := d.Policy.Schedule; sc != nil {
			if sc.EveryMS < 1000 || sc.WindowMS <= 0 || sc.Timezone != "Asia/Shanghai" {
				add("schedule requires interval >= 1000 ms, execution window and Asia/Shanghai timezone")
			}
		}
	}
	if d.Policy.TimeoutMS > 1000 || d.Policy.TimeoutMS < 0 {
		add("script timeout must be 1..1000 ms")
	}
	v.NativePlan = CompileNative(d)
	return v
}
func (s *Service) Published(ctx context.Context, id string, at int64) (model.Definition, error) {
	docs, e := s.Store.Versions(ctx, "definition", id)
	if e != nil {
		return model.Definition{}, e
	}
	var found model.Definition
	for _, doc := range docs {
		d, e := store.Decode[model.Definition](doc)
		if e != nil {
			return found, e
		}
		if at == 0 || d.EffectiveMS <= at {
			found = d
		}
	}
	if found.ID == "" {
		return found, store.ErrNotFound
	}
	return found, nil
}
func (s *Service) Version(ctx context.Context, id string, version int64) (model.Definition, error) {
	docs, e := s.Store.Versions(ctx, "definition", id)
	if e != nil {
		return model.Definition{}, e
	}
	for _, doc := range docs {
		definition, e := store.Decode[model.Definition](doc)
		if e != nil {
			return model.Definition{}, e
		}
		if definition.Version == version {
			return definition, nil
		}
	}
	return model.Definition{}, store.ErrNotFound
}
func (s *Service) SaveDraft(ctx context.Context, actor model.Actor, draft model.Draft, expected int64) (model.Draft, error) {
	if draft.ID == "" {
		draft.ID = draft.Definition.ID
	}
	if draft.ID == "" {
		return draft, errors.New("draft id is required")
	}
	draft.AuthorID = actor.UserID
	draft.Version = expected + 1
	draft.UpdatedMS = s.Store.Now().UnixMilli()
	draft.Definition.Status = "draft"
	e := s.Store.Write(ctx, func(t *store.Tx) error {
		if _, e := t.Put("draft", draft.ID, expected, draft); e != nil {
			return e
		}
		return t.Audit(actor, "definition.draft.save", draft.Definition.ID, draft.ID, draft)
	})
	return draft, e
}
func (s *Service) Publish(ctx context.Context, actor model.Actor, draftID string) (model.Definition, error) {
	doc, e := s.Store.Get(ctx, "draft", draftID)
	if e != nil {
		return model.Definition{}, e
	}
	draft, e := store.Decode[model.Draft](doc)
	if e != nil {
		return model.Definition{}, e
	}
	d := draft.Definition
	v := s.Validate(ctx, d)
	if !v.Valid {
		return d, fmt.Errorf("definition validation: %s", strings.Join(v.Errors, "; "))
	}
	d.Status = "published"
	d.Version = draft.BaseVersion + 1
	d.EffectiveMS = s.Store.Now().UnixMilli()
	e = s.Store.Write(ctx, func(t *store.Tx) error {
		current, e := t.Get("draft", draftID)
		if e != nil {
			return e
		}
		if current.Version != doc.Version {
			return store.ErrConflict
		}
		if _, e = t.Put("definition", d.ID, draft.BaseVersion, d); e != nil {
			return e
		}
		for _, o := range d.Outputs {
			if _, e = t.Put("catalogue", d.ID+"."+o.Key, -1, map[string]any{"id": d.ID + "." + o.Key, "definition_id": d.ID, "version": d.Version, "name": d.Name, "kind": d.Kind, "field": o, "selector": d.Selector, "group_id": d.GroupID, "status": "active", "query_path": "/api/sf/v1/data"}); e != nil {
				return e
			}
		}
		draft.Definition = d
		draft.BaseVersion = d.Version
		draft.Version = doc.Version + 1
		if _, e = t.Put("draft", draftID, doc.Version, draft); e != nil {
			return e
		}
		if e = t.Enqueue(fmt.Sprintf("tb-definition:%s:%d", d.ID, d.Version), "tb_definition", d.ID, d); e != nil {
			return e
		}
		if e = t.Enqueue(fmt.Sprintf("definition-sync:%s:%d", d.ID, d.Version), "edge_definition", d.ID, d); e != nil {
			return e
		}
		return t.Audit(actor, "definition.publish", d.ID, draftID, map[string]any{"definition": d, "validation": v})
	})
	return d, e
}
func (s *Service) Deactivate(ctx context.Context, actor model.Actor, id string, expected int64) (model.Definition, error) {
	d, e := s.Published(ctx, id, 0)
	if e != nil {
		return d, e
	}
	defs, e := s.Store.List(ctx, "definition")
	if e != nil {
		return d, e
	}
	for _, doc := range defs {
		other, e := store.Decode[model.Definition](doc)
		if e != nil {
			return d, e
		}
		if other.Status == "published" && has(other.Dependencies, id) {
			return d, fmt.Errorf("active dependent definition: %s", other.ID)
		}
	}
	d.Status = "inactive"
	d.Version = expected + 1
	d.EffectiveMS = s.Store.Now().UnixMilli()
	e = s.Store.Write(ctx, func(t *store.Tx) error {
		if _, e := t.Put("definition", id, expected, d); e != nil {
			return e
		}
		for _, o := range d.Outputs {
			if _, e := t.Put("catalogue", d.ID+"."+o.Key, -1, map[string]any{"id": d.ID + "." + o.Key, "definition_id": id, "group_id": d.GroupID, "status": "inactive", "field": o}); e != nil {
				return e
			}
		}
		return t.Audit(actor, "definition.deactivate", id, "", d)
	})
	return d, e
}
func CompileNative(d model.Definition) any {
	return map[string]any{"rule_chain": map[string]any{"name": "SmartFactory definition: " + d.ID, "type": "CORE", "definition_version": d.Version, "execution": "bounded_typed_extension", "selector": d.Selector}, "calculated_fields": NativeCalculations(d), "typed_graph": d.Nodes}
}

// NativeCalculations selects numeric window operators supported by TB's rolling
// calculated fields. Stateful integer and revision semantics use the typed engine.
func NativeCalculations(d model.Definition) []map[string]any {
	plans := []map[string]any{}
	if d.Kind != "analysis" || len(d.Selector.DeviceIDs) != 1 || len(d.Selector.Keys) != 1 || d.Selector.WindowMS <= 0 {
		return plans
	}
	for _, output := range d.Outputs {
		if output.Type != "number" {
			continue
		}
		id := output.NodeID
		for depth := 0; depth < len(d.Nodes); depth++ {
			var node model.Node
			for _, n := range d.Nodes {
				if n.ID == id {
					node = n
				}
			}
			if node.Type == "aggregate" {
				fn, _ := node.Params["function"].(string)
				if has([]string{"avg", "min", "max", "sum", "count"}, fn) {
					plans = append(plans, map[string]any{"device_id": d.Selector.DeviceIDs[0], "source_key": "sf_good." + d.Selector.Keys[0], "output_key": "sf_native." + d.ID + "." + output.Key, "function": fn, "window_ms": d.Selector.WindowMS})
				}
				break
			}
			if node.Type != "output" {
				break
			}
			next := ""
			for _, c := range d.Connections {
				if c.To == id {
					if next != "" {
						return plans
					}
					next = c.From
				}
			}
			if next == "" {
				break
			}
			id = next
		}
	}
	return plans
}
func integer(v any, fallback int64) int64 {
	n, ok := store.Number(v)
	if !ok {
		return fallback
	}
	if !n.IsInt() || !n.Num().IsInt64() {
		return fallback
	}
	return n.Num().Int64()
}
func Diff(before, after any) map[string]any {
	a, _ := json.MarshalIndent(before, "", "  ")
	b, _ := json.MarshalIndent(after, "", "  ")
	return map[string]any{"before": json.RawMessage(a), "after": json.RawMessage(b), "changed": string(a) != string(b)}
}
func timeout(d model.Definition) time.Duration {
	if d.Policy.TimeoutMS <= 0 {
		return 100 * time.Millisecond
	}
	return time.Duration(d.Policy.TimeoutMS) * time.Millisecond
}

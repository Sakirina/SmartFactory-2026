package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"competition2026/product/platform/internal/application/definitioncommit"
	"competition2026/product/platform/internal/compiledplan"
	"competition2026/product/platform/internal/rulecore"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Service struct {
	Store           *store.Store
	ControlEnabled  bool
	ObserveAnalysis func(context.Context, model.Observation) error
	mu              sync.Mutex
	strategyMu      sync.Mutex
	plans           sync.Map
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
func (s *Service) Validate(ctx context.Context, d model.Definition) model.Validation {
	_, result := rulecore.Compile(ctx, d)
	for _, issue := range s.dependencyIssues(ctx, d) {
		result.Valid = false
		result.Errors = append(result.Errors, issue)
	}
	result.NativePlan = CompileNative(d)
	return result
}

func (s *Service) dependencyIssues(ctx context.Context, d model.Definition) []string {
	at, _ := ctx.Value(historicalAtKey{}).(int64)
	if err := compiledplan.CheckDependencies(ctx, d, at, func(ctx context.Context, id string, at int64) (model.Definition, error) {
		other, err := s.Published(ctx, id, at)
		if err != nil {
			return other, err
		}
		if _, err := compiledplan.Load(ctx, s.Store, other); err == nil {
			return other, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return other, err
		}
		if other.ExecutionPlan != nil {
			return other, rulecore.Verify(other.ExecutionPlan, other)
		}
		_, err = compiledplan.Compile(ctx, other)
		return other, err
	}); err != nil {
		return []string{err.Error()}
	}
	return nil
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
	return definitioncommit.Commit(ctx, s.Store, actor, definitioncommit.Prepared{
		Draft: draft, DraftVersion: doc.Version, Validation: s.Validate(ctx, draft.Definition),
	})
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
	d.ExecutionPlan = nil
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

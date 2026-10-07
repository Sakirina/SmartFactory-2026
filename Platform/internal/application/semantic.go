package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"competition2026/product/platform/internal/application/definitioncommit"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"competition2026/product/platform/pkg/precise"
)

type SemanticInput struct {
	ExpectedVersion  int64 `json:"expected_version" minimum:"1" required:"true"`
	PublishedVersion int64 `json:"published_version,omitempty" minimum:"0"`
}

type ImpactInput struct {
	ExpectedVersion int64 `json:"expected_version" minimum:"1" required:"true"`
	Budget          int   `json:"budget,omitempty" minimum:"1" maximum:"2000" default:"512"`
}

func semanticIdentity(d model.Draft) model.SemanticIdentity {
	return model.SemanticIdentity{ID: d.ID, Version: d.Version, DefinitionID: d.Definition.ID, DefinitionVersion: d.Definition.Version, ContentHash: store.Hash(semanticDefinition(d.Definition))}
}

func (s *Business) semanticDraft(ctx context.Context, p identity.Principal, id string, version int64) (model.Draft, *revisions, error) {
	seen, err := s.authorize(ctx, p, "read", nil)
	if err != nil {
		return model.Draft{}, nil, err
	}
	doc, err := s.read(ctx, "draft", id, seen)
	if err != nil {
		return model.Draft{}, nil, err
	}
	draft, err := store.Decode[model.Draft](doc)
	if err != nil {
		return draft, nil, err
	}
	if version < 1 || doc.Version != version {
		return draft, nil, store.ErrConflict
	}
	// Invalid drafts still need inspection. Their own references are authorized
	// here; dependency graph diagnostics are produced by Impact below.
	if draft.Definition.GroupID == "" {
		return draft, nil, identity.ErrDenied
	}
	for _, resource := range definitionResources(draft.Definition) {
		if err = s.access().permit(ctx, p, "read", resource, seen); err != nil {
			return draft, nil, err
		}
	}
	return draft, seen, nil
}

func (s *Business) SemanticDifference(ctx context.Context, p identity.Principal, id string, input SemanticInput) (model.SemanticDifference, error) {
	var result model.SemanticDifference
	draft, seen, err := s.semanticDraft(ctx, p, id, input.ExpectedVersion)
	if err != nil {
		return result, err
	}
	var published model.Definition
	if input.PublishedVersion < 0 {
		return result, errors.New("published_version must be nonnegative")
	}
	if input.PublishedVersion == 0 {
		input.PublishedVersion = draft.BaseVersion
	}
	if input.PublishedVersion > 0 {
		versions, e := s.Store.Versions(ctx, "definition", draft.Definition.ID)
		if e != nil {
			return result, e
		}
		found := false
		for _, doc := range versions {
			d, e := store.Decode[model.Definition](doc)
			if e != nil {
				return result, e
			}
			if d.Version == input.PublishedVersion {
				published = d
				found = true
				break
			}
		}
		if !found {
			return result, store.ErrNotFound
		}
		for _, resource := range definitionResources(published) {
			if e := s.access().permit(ctx, p, "read", resource, seen); e != nil {
				return result, e
			}
		}
	}
	// Authorize transitive dependencies without returning hidden identifiers.
	for _, d := range []model.Definition{published, draft.Definition} {
		if d.ID != "" {
			if err = s.authorizeSemanticDependencies(ctx, p, d, seen, map[string]bool{}, 0); err != nil {
				return result, err
			}
		}
	}
	result = model.SemanticDifference{Draft: semanticIdentity(draft), Published: model.SemanticIdentity{ID: published.ID, Version: published.Version, DefinitionID: published.ID, DefinitionVersion: published.Version, ContentHash: store.Hash(semanticDefinition(published))}, Changes: []model.SemanticChange{}}
	before, after := semanticDefinition(published), semanticDefinition(draft.Definition)
	var walk func(string, any, any, bool, bool)
	walk = func(path string, a, b any, aexists, bexists bool) {
		if aexists == bexists && semanticEqual(a, b) {
			return
		}
		am, aok := a.(map[string]any)
		bm, bok := b.(map[string]any)
		if aok && bok {
			keys := map[string]bool{}
			for k := range am {
				keys[k] = true
			}
			for k := range bm {
				keys[k] = true
			}
			ordered := []string{}
			for k := range keys {
				ordered = append(ordered, k)
			}
			sort.Strings(ordered)
			for _, k := range ordered {
				av, ae := am[k]
				bv, be := bm[k]
				walk(path+"/"+strings.NewReplacer("~", "~0", "/", "~1").Replace(k), av, bv, ae, be)
			}
			return
		}
		change := "changed"
		if !aexists {
			change = "added"
		}
		if !bexists {
			change = "removed"
		}
		category := strings.Split(strings.TrimPrefix(path, "/"), "/")[0]
		at, bt := semanticType(a), semanticType(b)
		if !aexists {
			at = "absent"
		}
		if !bexists {
			bt = "absent"
		}
		result.Changes = append(result.Changes, model.SemanticChange{Category: category, Path: path, Change: change, Before: a, After: b, BeforeType: at, AfterType: bt, Unit: semanticUnit(path, draft.Definition, published)})
	}
	walk("", before, after, true, true)
	result.Equivalent = len(result.Changes) == 0
	err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
	return result, err
}

func (s *Business) authorizeSemanticDependencies(ctx context.Context, p identity.Principal, d model.Definition, seen *revisions, visited map[string]bool, depth int) error {
	if visited[d.ID] {
		return nil
	}
	visited[d.ID] = true
	if depth > 64 || len(visited) > 2000 {
		return errors.New("semantic dependency budget exceeded")
	}
	for _, id := range d.Dependencies {
		doc, err := s.Store.Get(ctx, "definition", id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err = seen.remember("definition", id, doc.Version); err != nil {
			return err
		}
		other, err := store.Decode[model.Definition](doc)
		if err != nil {
			return err
		}
		if other.GroupID == "" {
			return identity.ErrDenied
		}
		for _, r := range definitionResources(other) {
			if err = s.access().permit(ctx, p, "read", r, seen); err != nil {
				return err
			}
		}
		if err = s.authorizeSemanticDependencies(ctx, p, other, seen, visited, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func semanticDefinition(d model.Definition) map[string]any {
	raw, _ := json.Marshal(d)
	var result map[string]any
	_ = store.DecodeJSON(raw, &result)
	for _, key := range []string{"version", "status", "effective_ms", "execution_plan"} {
		delete(result, key)
	}
	nodes := map[string]any{}
	for _, n := range d.Nodes {
		r, _ := json.Marshal(n)
		var v map[string]any
		_ = store.DecodeJSON(r, &v)
		delete(v, "position")
		delete(v, "id")
		for _, key := range []string{"inputs", "outputs"} {
			if ports, ok := v[key].([]any); ok {
				sort.Slice(ports, func(i, j int) bool { return store.Hash(ports[i]) < store.Hash(ports[j]) })
			}
		}
		if existing, ok := nodes[n.ID]; ok {
			if list, ok := existing.([]any); ok {
				nodes[n.ID] = append(list, v)
			} else {
				nodes[n.ID] = []any{existing, v}
			}
		} else {
			nodes[n.ID] = v
		}
	}
	result["nodes"] = nodes
	connections := map[string]any{}
	for _, c := range d.Connections {
		if c.FromPort == "" {
			c.FromPort = "value"
		}
		if c.ToPort == "" {
			c.ToPort = "value"
		}
		identity, _ := json.Marshal([]string{c.From, c.FromPort, c.To, c.ToPort})
		key := string(identity)
		value := map[string]any{"from": c.From, "from_port": c.FromPort, "to": c.To, "to_port": c.ToPort}
		if old, ok := connections[key]; ok {
			if duplicates, ok := old.([]any); ok {
				connections[key] = append(duplicates, value)
			} else {
				connections[key] = []any{old, value}
			}
		} else {
			connections[key] = value
		}
	}
	result["connections"] = connections
	outputs := map[string]any{}
	for _, o := range d.Outputs {
		r, _ := json.Marshal(o)
		var v map[string]any
		_ = store.DecodeJSON(r, &v)
		delete(v, "key")
		if existing, ok := outputs[o.Key]; ok {
			if list, ok := existing.([]any); ok {
				outputs[o.Key] = append(list, v)
			} else {
				outputs[o.Key] = []any{existing, v}
			}
		} else {
			outputs[o.Key] = v
		}
	}
	result["outputs"] = outputs
	result["dependencies"] = uniqueHistory(d.Dependencies)
	selector := result["selector"].(map[string]any)
	selector["device_ids"] = uniqueHistory(d.Selector.DeviceIDs)
	selector["keys"] = uniqueHistory(d.Selector.Keys)
	policy := result["policy"].(map[string]any)
	for key, values := range map[string][]string{"edge_ids": d.Policy.EdgeIDs, "channels": d.Policy.Channels, "recipients": d.Policy.Recipients} {
		policy[key] = uniqueHistory(values)
	}
	return result
}

func semanticEqual(a, b any) bool {
	an, aok := precise.Number(a)
	bn, bok := precise.Number(b)
	if aok || bok {
		return aok && bok && an.Cmp(bn) == 0
	}
	if am, ok := a.(map[string]any); ok {
		bm, ok := b.(map[string]any)
		if !ok || len(am) != len(bm) {
			return false
		}
		for k, v := range am {
			w, exists := bm[k]
			if !exists || !semanticEqual(v, w) {
				return false
			}
		}
		return true
	}
	if aa, ok := a.([]any); ok {
		ba, ok := b.([]any)
		if !ok || len(aa) != len(ba) {
			return false
		}
		for i, v := range aa {
			if !semanticEqual(v, ba[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}
func semanticType(value any) string {
	if value == nil {
		return "null"
	}
	if number, ok := precise.Number(value); ok {
		if number.IsInt() {
			return "integer"
		}
		return "number"
	}
	switch value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any, []string:
		return "array"
	}
	return fmt.Sprintf("%T", value)
}
func semanticUnit(path string, defs ...model.Definition) string {
	parts := strings.Split(path, "/")
	if len(parts) < 4 || parts[1] != "nodes" {
		return ""
	}
	id := strings.NewReplacer("~1", "/", "~0", "~").Replace(parts[2])
	key := parts[len(parts)-1]
	for _, d := range defs {
		for _, node := range d.Nodes {
			if node.ID != id {
				continue
			}
			for _, metadata := range model.NodeCatalog() {
				if metadata.Type != node.Type {
					continue
				}
				for _, parameter := range metadata.Parameters {
					if parameter.Key == key {
						return parameter.Unit
					}
				}
			}
		}
	}
	return ""
}

func (s *Business) Impact(ctx context.Context, p identity.Principal, id string, input ImpactInput) (model.ImpactAnalysis, error) {
	var result model.ImpactAnalysis
	draft, seen, err := s.semanticDraft(ctx, p, id, input.ExpectedVersion)
	if err != nil {
		return result, err
	}
	if input.Budget == 0 {
		input.Budget = 512
	}
	if input.Budget < 1 || input.Budget > 2000 {
		return result, errors.New("impact budget must be between 1 and 2000")
	}
	result = model.ImpactAnalysis{Draft: semanticIdentity(draft), References: []model.ImpactReference{}, Issues: []model.ImpactIssue{}, Budget: input.Budget, Complete: true}
	docs, err := s.Store.List(ctx, "definition")
	if err != nil {
		return result, err
	}
	if len(docs) > 5000 {
		return result, errors.New("impact catalogue exceeds 5000 definitions")
	}
	definitions := map[string]model.Definition{draft.Definition.ID: draft.Definition}
	visible := map[string]bool{draft.Definition.ID: true}
	versions := map[string]int64{}
	for _, doc := range docs {
		d, e := store.Decode[model.Definition](doc)
		if e != nil {
			return result, e
		}
		versions[d.ID] = doc.Version
		if d.ID == draft.Definition.ID {
			continue
		}
		definitions[d.ID] = d
		can := d.GroupID != ""
		for _, r := range definitionResources(d) {
			if e = s.Identity.Permit(ctx, p, "read", r); e != nil {
				if errors.Is(e, identity.ErrDenied) {
					can = false
					break
				}
				return result, e
			}
		}
		visible[d.ID] = can
	}
	// Pin collection membership so newly published consumers cannot disappear
	// from a result that claims complete dependency coverage.
	guards := seen.guards()
	guards = append(guards, definitioncommit.Revision{Kind: "definition", Members: versions})
	visited := map[string]bool{}
	visiting := map[string]bool{}
	references := map[string]bool{}
	add := func(r model.ImpactReference) {
		key := store.Hash(r)
		if !references[key] {
			if len(result.References) >= input.Budget {
				if result.Complete {
					result.Issues = append(result.Issues, model.ImpactIssue{Code: "budget_exceeded", Location: "references", Description: "increase the budget to inspect remaining references"})
				}
				result.Complete = false
				return
			}
			references[key] = true
			result.References = append(result.References, r)
		}
	}
	issue := func(code, target, location, description string) {
		result.Issues = append(result.Issues, model.ImpactIssue{Code: code, ID: target, Location: location, Description: description})
		result.Complete = false
	}
	var visit func(string, string, string) error
	visit = func(target, via, location string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if result.Visited >= input.Budget {
			issue("budget_exceeded", "", location, "increase the budget to inspect remaining references")
			return nil
		}
		d, exists := definitions[target]
		if !exists {
			issue("missing_reference", target, location, "referenced definition is unavailable")
			return nil
		}
		if !visible[target] {
			issue("restricted_reference", "", location, "a referenced definition is outside the current access scope")
			return nil
		}
		if visiting[target] {
			issue("cycle", target, location, "dependency cycle found")
			return nil
		}
		add(model.ImpactReference{Kind: "definition", ID: target, Version: d.Version, Via: via, Location: location})
		if visited[target] {
			return nil
		}
		visited[target] = true
		visiting[target] = true
		result.Visited++
		for _, r := range definitionResources(d) {
			if err = s.access().permit(ctx, p, "read", r, seen); err != nil {
				return err
			}
			edoc, e := s.Store.Get(ctx, "entity", r)
			if errors.Is(e, store.ErrNotFound) {
				issue("missing_resource", r, target, "referenced entity is unavailable")
				continue
			}
			if e != nil {
				return e
			}
			entity, e := store.Decode[model.Entity](edoc)
			if e != nil {
				return e
			}
			if entity.Kind == "device" {
				add(model.ImpactReference{Kind: "device", ID: r, Version: edoc.Version, Via: target, Location: "definition.resources"})
			}
		}
		for _, o := range d.Outputs {
			add(model.ImpactReference{Kind: "output", ID: d.ID + "." + o.Key, Version: d.Version, Via: target, Location: "outputs/" + o.Key})
		}
		for _, dep := range d.Dependencies {
			if err := visit(dep, target, "dependencies"); err != nil {
				return err
			}
		}
		delete(visiting, target)
		return nil
	}
	if err = visit(draft.Definition.ID, "", "draft"); err != nil {
		return result, err
	}
	affected := map[string]bool{draft.Definition.ID: true}
	changed := true
	for changed {
		changed = false
		ids := []string{}
		for target := range definitions {
			ids = append(ids, target)
		}
		sort.Strings(ids)
		for _, target := range ids {
			d := definitions[target]
			if affected[target] {
				continue
			}
			for _, dep := range d.Dependencies {
				if !affected[dep] {
					continue
				}
				affected[target] = true
				changed = true
				if visible[target] {
					via := dep
					if !visible[dep] {
						via = ""
						issue("restricted_reference", "", "dependent_rule", "an affected path contains a definition outside the current access scope")
					}
					if err = visit(target, via, "dependent_rule"); err != nil {
						return result, err
					}
				}
				break
			}
		}
	}
	for _, kind := range []string{"dashboard", "work_order"} {
		rows, e := s.Store.List(ctx, kind)
		if e != nil {
			return result, e
		}
		if len(rows) > 5000 {
			return result, errors.New("impact usage catalogue exceeds 5000 objects")
		}
		members := map[string]int64{}
		for _, doc := range rows {
			members[doc.ID] = doc.Version
			var value map[string]any
			if e = store.DecodeJSON(doc.Data, &value); e != nil {
				return result, e
			}
			group, _ := value["group_id"].(string)
			if group == "" || s.access().permit(ctx, p, "read", group, seen) != nil {
				continue
			}
			if kind == "dashboard" {
				dashboard, e := store.Decode[model.Dashboard](doc)
				if e != nil {
					return result, e
				}
				keys := append([]string{}, dashboard.Keys...)
				for _, metric := range dashboard.Metrics {
					keys = append(keys, metric.Key)
				}
				for target := range affected {
					if !visible[target] {
						continue
					}
					for _, key := range keys {
						if strings.HasPrefix(key, target+".") {
							add(model.ImpactReference{Kind: kind, ID: doc.ID, Version: doc.Version, Via: target, Location: "output_usage"})
						}
					}
				}
			}
			if kind == "work_order" {
				w, e := store.Decode[model.WorkOrder](doc)
				if e != nil {
					return result, e
				}
				if _, e = s.linkedResources(ctx, p, w.GroupID, w.AlarmIDs, w.ExecutionIDs, seen); e != nil {
					if errors.Is(e, identity.ErrDenied) {
						continue
					}
					return result, e
				}
				for _, ref := range w.AlarmIDs {
					d, e := s.Store.Get(ctx, "alarm", ref)
					if errors.Is(e, store.ErrNotFound) {
						continue
					}
					if e != nil {
						return result, e
					}
					a, e := store.Decode[model.Alarm](d)
					if e != nil {
						return result, e
					}
					if affected[a.DefinitionID] && s.access().permit(ctx, p, "read", a.EntityID, seen) == nil {
						add(model.ImpactReference{Kind: kind, ID: doc.ID, Version: doc.Version, Via: a.DefinitionID, Location: "alarm_ids"})
					}
				}
				for _, ref := range w.ExecutionIDs {
					d, e := s.Store.Get(ctx, "execution", ref)
					if errors.Is(e, store.ErrNotFound) {
						continue
					}
					if e != nil {
						return result, e
					}
					x, e := store.Decode[model.Execution](d)
					if e != nil {
						return result, e
					}
					if affected[x.DefinitionID] && visible[x.DefinitionID] {
						add(model.ImpactReference{Kind: kind, ID: doc.ID, Version: doc.Version, Via: x.DefinitionID, Location: "execution_ids"})
					}
				}
			}
		}
		guards = append(guards, definitioncommit.Revision{Kind: kind, Members: members})
	}
	sort.Slice(result.References, func(i, j int) bool { return store.Hash(result.References[i]) < store.Hash(result.References[j]) })
	guards = append(guards, seen.guards()...)
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if e := seen.check(tx); e != nil {
			return e
		}
		return definitioncommit.CheckRevisions(tx, guards)
	})
	return result, err
}

package thingsboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type ruleMapping struct {
	ID           EntityID `json:"id"`
	DefinitionID string   `json:"definition_id"`
	Version      int64    `json:"version"`
	Status       string   `json:"status"`
}

func (a *Adapter) definitionTargets(ctx context.Context, points []model.Observation) ([]string, error) {
	docs, err := a.Store.List(ctx, "definition")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	selector := &engine.Service{Store: a.Store}
	for _, doc := range docs {
		d, err := store.Decode[model.Definition](doc)
		if err != nil {
			return nil, err
		}
		if d.Status != "published" {
			continue
		}
		for _, p := range points {
			if p.DefinitionID == d.ID {
				continue
			}
			ok, err := selector.Matches(ctx, d, p)
			if err != nil {
				return nil, err
			}
			if ok {
				ids = append(ids, d.ID)
				break
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}
func (a *Adapter) installDefinition(ctx context.Context, d model.Definition) error {
	var page struct {
		Data []map[string]any `json:"data"`
	}
	name := "SmartFactory definition: " + d.ID
	if err := a.Client.Do(ctx, "GET", "/api/ruleChains?pageSize=100&page=0&type=CORE&textSearch="+url.QueryEscape(name), nil, &page); err != nil {
		return err
	}
	var chain map[string]any
	for _, item := range page.Data {
		if item["name"] == name {
			chain = item
			break
		}
	}
	if chain == nil {
		chain = map[string]any{"name": name, "type": "CORE", "debugMode": false}
	}
	chain["additionalInfo"] = map[string]any{"sf_definition_id": d.ID, "sf_definition_version": d.Version, "sf_status": d.Status, "sf_graph": d}
	if err := a.Client.Do(ctx, "POST", "/api/ruleChain", chain, &chain); err != nil {
		return err
	}
	raw, _ := json.Marshal(chain["id"])
	var id EntityID
	if err := json.Unmarshal(raw, &id); err != nil || id.ID == "" {
		return errors.New("definition rule chain id missing")
	}
	quoted, _ := json.Marshal(d.ID)
	filter := "return msg.sf_definition_ids != null && msg.sf_definition_ids.contains(" + string(quoted) + ");"
	callback := map[string]any{"restEndpointUrlPattern": strings.TrimRight(a.CallbackURL, "/") + "/internal/native/evaluate?definition_id=" + url.QueryEscape(d.ID), "requestMethod": "POST", "headers": map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + a.ServiceToken}, "useSimpleClientHttpFactory": false, "readTimeoutMs": 14000, "maxParallelRequestsCount": 64, "maxInMemoryBufferSizeInKb": 2048, "credentials": map[string]string{"type": "anonymous"}}
	nodes := []any{
		map[string]any{"name": "Definition selector", "type": "org.thingsboard.rule.engine.filter.TbJsFilterNode", "configuration": map[string]any{"scriptLang": "TBEL", "tbelScript": filter, "jsScript": filter}},
		map[string]any{"name": "Typed graph: " + d.Name, "type": "org.thingsboard.rule.engine.rest.TbRestApiCallNode", "configuration": callback},
		map[string]any{"name": "Success", "type": "org.thingsboard.rule.engine.flow.TbRuleChainOutputNode", "configuration": map[string]any{}},
		map[string]any{"name": "Failure", "type": "org.thingsboard.rule.engine.flow.TbRuleChainOutputNode", "configuration": map[string]any{}},
	}
	connections := []any{}
	for _, c := range []struct {
		from, to int
		kind     string
	}{{0, 1, "True"}, {0, 2, "False"}, {0, 3, "Failure"}, {1, 2, "Success"}, {1, 3, "Failure"}} {
		connections = append(connections, map[string]any{"fromIndex": c.from, "toIndex": c.to, "type": c.kind})
	}
	if err := a.Client.Do(ctx, "POST", "/api/ruleChain/metadata", map[string]any{"ruleChainId": id, "firstNodeIndex": 0, "nodes": nodes, "connections": connections, "ruleChainConnections": []any{}}, nil); err != nil {
		return err
	}
	if err := a.installCalculations(ctx, d); err != nil {
		return err
	}
	if _, err := a.Store.Put(ctx, "tb_rule_chain", d.ID, -1, ruleMapping{id, d.ID, d.Version, d.Status}); err != nil {
		return err
	}
	return nil
}

func (a *Adapter) ReconcileDefinitions(ctx context.Context) error {
	docs, err := a.Store.List(ctx, "definition")
	if err != nil {
		return err
	}
	for _, doc := range docs {
		d, err := store.Decode[model.Definition](doc)
		if err != nil {
			return err
		}
		if saved, e := a.Store.Get(ctx, "tb_rule_chain", d.ID); e == nil {
			mapping, e := store.Decode[ruleMapping](saved)
			if e != nil {
				return e
			}
			if mapping.Version >= d.Version {
				continue
			}
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if err = a.Definition(ctx, d); err != nil {
			return err
		}
	}
	return nil
}
func (a *Adapter) installCalculations(ctx context.Context, d model.Definition) error {
	for _, plan := range engine.NativeCalculations(d) {
		doc, err := a.Store.Get(ctx, "entity", plan["device_id"].(string))
		if err != nil {
			return err
		}
		entity, err := store.Decode[model.Entity](doc)
		if err != nil {
			return err
		}
		mapped, err := a.EnsureEntity(ctx, entity)
		if err != nil {
			return err
		}
		var page struct {
			Data []map[string]any `json:"data"`
		}
		if err = a.Client.Do(ctx, "GET", fmt.Sprintf("/api/%s/%s/calculatedFields?pageSize=100&page=0", mapped.Native.EntityType, mapped.Native.ID), nil, &page); err != nil {
			return err
		}
		name := "SmartFactory: " + d.ID + ":" + plan["output_key"].(string)
		var cf map[string]any
		for _, item := range page.Data {
			if item["name"] == name {
				cf = item
				break
			}
		}
		if d.Status != "published" {
			if cf != nil {
				raw, _ := json.Marshal(cf["id"])
				var id EntityID
				_ = json.Unmarshal(raw, &id)
				if err = a.Client.Do(ctx, "DELETE", "/api/calculatedField/"+id.ID, nil, nil); err != nil {
					return err
				}
			}
			continue
		}
		if cf == nil {
			cf = map[string]any{"entityId": mapped.Native, "name": name}
		}
		out, _ := json.Marshal(plan["output_key"])
		cf["type"] = "SCRIPT"
		cf["configurationVersion"] = 1
		cf["additionalInfo"] = map[string]any{"sf_definition_id": d.ID, "sf_version": d.Version, "sf_good_only": true}
		cf["configuration"] = map[string]any{"type": "SCRIPT", "arguments": map[string]any{"values": map[string]any{"refEntityKey": map[string]string{"key": plan["source_key"].(string), "type": "TS_ROLLING"}, "limit": 1000, "timeWindow": plan["window_ms"]}}, "expression": "return {" + string(out) + ": values." + plan["function"].(string) + "()};", "output": map[string]any{"type": "TIME_SERIES", "strategy": map[string]any{"type": "IMMEDIATE", "ttl": a.Store.Policy().Retention.RawDays * 86400, "saveTimeSeries": true, "saveLatest": true, "sendWsUpdate": true, "processCfs": false}}}
		if err = a.Client.Do(ctx, "POST", "/api/calculatedField", cf, &cf); err != nil {
			return err
		}
		if _, err = a.Store.Put(ctx, "tb_calculated_field", name, -1, cf); err != nil {
			return err
		}
	}
	return nil
}

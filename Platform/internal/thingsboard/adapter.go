package thingsboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type EntityID struct {
	ID         string `json:"id"`
	EntityType string `json:"entityType"`
}
type Mapping struct {
	BusinessID    string   `json:"business_id"`
	Native        EntityID `json:"native"`
	EntityVersion int64    `json:"entity_version"`
}
type Adapter struct {
	Client                    *Client
	Store                     *store.Store
	CallbackURL, ServiceToken string
	Edge                      bool
	mu                        sync.Mutex
}

func (a *Adapter) EnsureEntity(ctx context.Context, e model.Entity) (Mapping, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var mapping Mapping
	doc, err := a.Store.Get(ctx, "tb_mapping", e.ID)
	if err == nil {
		mapping, err = store.Decode[Mapping](doc)
		if err != nil || mapping.EntityVersion >= e.Version {
			return mapping, err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return mapping, err
	}
	if e.Kind == "edge" {
		return mapping, nil
	}
	typ := "device"
	if e.Kind != "device" {
		typ = "asset"
	}
	name := "sf:" + e.ID
	var native map[string]any
	if mapping.Native.ID != "" {
		err = a.Client.Do(ctx, "GET", "/api/"+typ+"/"+mapping.Native.ID, nil, &native)
	} else {
		err = a.Client.Do(ctx, "GET", "/api/tenant/"+typ+"s?"+typ+"Name="+url.QueryEscape(name), nil, &native)
	}
	if err != nil {
		var h HTTPError
		if !errors.As(err, &h) || h.Status != 404 {
			return mapping, err
		}
		native = map[string]any{"name": name, "type": "SmartFactory", "label": e.Name}
	}
	native["label"] = e.Name
	native["additionalInfo"] = map[string]any{"sf_id": e.ID, "sf_entity_revision": e.Version, "sf_edge_id": e.EdgeID, "description": e.Name}
	if err = a.Client.Do(ctx, "POST", "/api/"+typ, native, &native); err != nil {
		return mapping, err
	}
	b, _ := json.Marshal(native["id"])
	if err = json.Unmarshal(b, &mapping.Native); err != nil || mapping.Native.ID == "" {
		return mapping, errors.New("ThingsBoard returned no entity id")
	}
	mapping.BusinessID, mapping.EntityVersion = e.ID, e.Version
	attributes := map[string]any{"sf_id": e.ID, "sf_entity_revision": e.Version, "sf_edge_id": e.EdgeID, "sf_sampling_ms": e.SamplingMS}
	if len(e.Tags) > 0 {
		attributes["sf_tags"] = e.Tags
	}
	err = a.Client.Do(ctx, "POST", fmt.Sprintf("/api/plugins/telemetry/%s/%s/attributes/SERVER_SCOPE", mapping.Native.EntityType, mapping.Native.ID), attributes, nil)
	if err != nil {
		return mapping, err
	}
	_, err = a.Store.Put(ctx, "tb_mapping", e.ID, -1, mapping)
	return mapping, err
}

// Project saves telemetry in TB and obtains a reply only after the native rule chain
// has called the SmartFactory extension. Redelivery keeps observation IDs intact.
func (a *Adapter) Project(ctx context.Context, points []model.Observation) error {
	documents, err := a.Store.List(ctx, "definition")
	if err != nil {
		return err
	}
	definitions := []model.Definition{}
	for _, doc := range documents {
		definition, err := store.Decode[model.Definition](doc)
		if err != nil {
			return err
		}
		if definition.Status == "published" {
			definitions = append(definitions, definition)
		}
	}
	selector := &engine.Service{Store: a.Store}
	groups := map[string][]model.Observation{}
	for _, p := range points {
		groups[p.DeviceID] = append(groups[p.DeviceID], p)
	}
	for device, values := range groups {
		doc, err := a.Store.Get(ctx, "entity", device)
		if err != nil {
			return err
		}
		entity, err := store.Decode[model.Entity](doc)
		if err != nil {
			return err
		}
		mapping, err := a.EnsureEntity(ctx, entity)
		if err != nil {
			return err
		}
		telemetry := []any{}
		for _, p := range values {
			telemetry = append(telemetry, map[string]any{"ts": p.ObservedMS, "values": telemetryFields(p)})
		}
		base := mapping.Native.EntityType + "/" + mapping.Native.ID
		if err = a.Client.Do(ctx, "POST", "/api/plugins/telemetry/"+base+"/timeseries/ANY", telemetry, nil); err != nil {
			return fmt.Errorf("native telemetry for %s: %w", device, err)
		}
		for _, definition := range definitions {
			selected := []model.Observation{}
			for _, point := range values {
				if point.DefinitionID == definition.ID {
					continue
				}
				matches, err := selector.Matches(ctx, definition, point)
				if err != nil {
					return err
				}
				if matches {
					selected = append(selected, point)
				}
			}
			// Each native branch receives only its selected input. Bounded batches
			// avoid multiplying a large payload by every definition on the device.
			for start := 0; start < len(selected); start += 64 {
				batch := selected[start:min(start+64, len(selected))]
				targets := []string{definition.ID}
				batchID := store.Hash([]any{batch, targets})
				var reply struct {
					Committed bool   `json:"committed"`
					Error     string `json:"error"`
				}
				if err = a.Client.Do(ctx, "POST", "/api/rule-engine/"+base+"/15000", map[string]any{"sf_observations": batch, "sf_definition_ids": targets, "sf_batch_id": batchID, "committed": false}, &reply); err != nil {
					return fmt.Errorf("native evaluation for %s/%s: %w", device, definition.ID, err)
				}
				if !reply.Committed {
					return fmt.Errorf("native rule callback was not committed: %s", reply.Error)
				}
			}
		}
	}
	return nil
}

// ThingsBoard rejects scalar null and stores integers as signed int64. Preserve
// absent readings through quality metadata and larger integers as decimal text.
func telemetryFields(p model.Observation) map[string]any {
	fields := map[string]any{p.Key + "__quality": p.Quality, p.Key + "__revision": p.Revision, p.Key + "__present": p.Value != nil, p.Key + "__observation_id": p.ID, p.Key + "__time_source": p.TimeSource}
	if p.QualityReason != "" {
		fields[p.Key+"__quality_reason"] = p.QualityReason
	}
	if p.Value == nil {
		return fields
	}
	value := p.Value
	number, numeric := store.Number(p.Value)
	if numeric && number.IsInt() && !number.Num().IsInt64() {
		value = number.Num().String()
		fields[p.Key+"__encoding"] = "integer_string"
	}
	fields[p.Key] = value
	if p.Quality == "GOOD" && p.DefinitionID == "" && numeric && fields[p.Key+"__encoding"] == nil {
		fields["sf_good."+p.Key] = value
	}
	return fields
}

func (a *Adapter) InstallRoot(ctx context.Context) error {
	if a.CallbackURL == "" || a.ServiceToken == "" {
		return errors.New("native callback URL and service token are required")
	}
	var page struct {
		Data []map[string]any `json:"data"`
	}
	if err := a.Client.Do(ctx, "GET", "/api/ruleChains?pageSize=100&page=0&type=CORE", nil, &page); err != nil {
		return err
	}
	var chain map[string]any
	for _, c := range page.Data {
		if c["name"] == "SmartFactory transport" {
			chain = c
			break
		}
	}
	if chain == nil {
		chain = map[string]any{"name": "SmartFactory transport", "type": "CORE", "debugMode": false, "additionalInfo": map[string]string{"sf_managed": "transport-v1"}}
		if err := a.Client.Do(ctx, "POST", "/api/ruleChain", chain, &chain); err != nil {
			return err
		}
	}
	idBytes, _ := json.Marshal(chain["id"])
	var id EntityID
	if err := json.Unmarshal(idBytes, &id); err != nil || id.ID == "" {
		return errors.New("native root rule chain id missing")
	}
	callback := map[string]any{"restEndpointUrlPattern": strings.TrimRight(a.CallbackURL, "/") + "/internal/native/evaluate", "requestMethod": "POST", "headers": map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + a.ServiceToken}, "useSimpleClientHttpFactory": false, "readTimeoutMs": 14000, "maxParallelRequestsCount": 64, "maxInMemoryBufferSizeInKb": 2048, "credentials": map[string]string{"type": "anonymous"}}
	nodes := []any{
		map[string]any{"name": "Message type", "type": "org.thingsboard.rule.engine.filter.TbMsgTypeSwitchNode", "configuration": map[string]any{}},
		map[string]any{"name": "SF typed engine extension", "type": "org.thingsboard.rule.engine.rest.TbRestApiCallNode", "configuration": callback},
		map[string]any{"name": "Committed reply", "type": "org.thingsboard.rule.engine.rest.TbSendRestApiCallReplyNode", "configuration": map[string]any{}},
		map[string]any{"name": "Save native telemetry", "type": "org.thingsboard.rule.engine.telemetry.TbMsgTimeseriesNode", "configurationVersion": 1, "configuration": map[string]any{"defaultTTL": 2592000, "useServerTs": false, "processingSettings": map[string]string{"type": "ON_EVERY_MESSAGE"}}},
		map[string]any{"name": "Save native attributes", "type": "org.thingsboard.rule.engine.telemetry.TbMsgAttributesNode", "configurationVersion": 3, "configuration": map[string]any{"scope": "CLIENT_SCOPE", "processingSettings": map[string]string{"type": "ON_EVERY_MESSAGE"}, "updateAttributesOnlyOnValueChange": true}},
	}
	connections := []any{}
	for _, c := range []struct {
		From, To int
		Relation string
	}{{0, 1, "REST API request"}, {1, 2, "Success"}, {1, 2, "Failure"}, {0, 3, "Post telemetry"}, {0, 4, "Post attributes"}} {
		connections = append(connections, map[string]any{"fromIndex": c.From, "toIndex": c.To, "type": c.Relation})
	}
	mappings, err := a.Store.List(ctx, "tb_rule_chain")
	if err != nil {
		return err
	}
	if len(mappings) > 0 {
		connections = []any{map[string]any{"fromIndex": 0, "toIndex": 3, "type": "Post telemetry"}, map[string]any{"fromIndex": 0, "toIndex": 4, "type": "Post attributes"}}
		switchScript := "if (msg.sf_definition_ids == null || msg.sf_definition_ids.size() == 0) { return ['__empty__']; } return msg.sf_definition_ids;"
		nodes = append(nodes, map[string]any{"name": "Applicable definitions", "type": "org.thingsboard.rule.engine.filter.TbJsSwitchNode", "configuration": map[string]any{"scriptLang": "TBEL", "tbelScript": switchScript, "jsScript": switchScript}}, map[string]any{"name": "All definitions committed", "type": "org.thingsboard.rule.engine.filter.TbJsFilterNode", "configuration": map[string]any{"scriptLang": "TBEL", "tbelScript": "return msg.committed == true;", "jsScript": "return msg.committed === true;"}})
		connections = append(connections, map[string]any{"fromIndex": 0, "toIndex": 5, "type": "REST API request"}, map[string]any{"fromIndex": 5, "toIndex": 1, "type": "__empty__"}, map[string]any{"fromIndex": 1, "toIndex": 2, "type": "Success"}, map[string]any{"fromIndex": 1, "toIndex": 2, "type": "Failure"}, map[string]any{"fromIndex": 6, "toIndex": 2, "type": "True"}, map[string]any{"fromIndex": 5, "toIndex": 2, "type": "Failure"})
		for _, doc := range mappings {
			m, e := store.Decode[ruleMapping](doc)
			if e != nil {
				return e
			}
			if m.Status != "published" {
				continue
			}
			index := len(nodes)
			nodes = append(nodes, map[string]any{"name": "Definition: " + m.DefinitionID, "type": "org.thingsboard.rule.engine.flow.TbRuleChainInputNode", "configurationVersion": 1, "configuration": map[string]any{"ruleChainId": m.ID.ID, "forwardMsgToDefaultRuleChain": false}})
			connections = append(connections, map[string]any{"fromIndex": 5, "toIndex": index, "type": m.DefinitionID}, map[string]any{"fromIndex": index, "toIndex": 2, "type": "Failure"}, map[string]any{"fromIndex": index, "toIndex": 6, "type": "Success"})
		}
	}
	meta := map[string]any{"ruleChainId": id, "firstNodeIndex": 0, "nodes": nodes, "connections": connections, "ruleChainConnections": []any{}}
	if err := a.Client.Do(ctx, "POST", "/api/ruleChain/metadata", meta, nil); err != nil {
		return err
	}
	return a.Client.Do(ctx, "POST", "/api/ruleChain/"+id.ID+"/root", nil, nil)
}

func (a *Adapter) Definition(ctx context.Context, d model.Definition) error {
	if doc, err := a.Store.Get(ctx, "tb_rule_chain", d.ID); err == nil {
		mapped, err := store.Decode[ruleMapping](doc)
		if err != nil {
			return err
		}
		if mapped.Version > d.Version {
			return nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	entity := model.Entity{ID: "definition:" + d.ID, Kind: "asset", Name: d.Name, Version: d.Version}
	m, err := a.EnsureEntity(ctx, entity)
	if err != nil {
		return err
	}
	if err = a.Client.Do(ctx, "POST", "/api/plugins/telemetry/ASSET/"+m.Native.ID+"/attributes/SERVER_SCOPE", map[string]any{"sf_definition": d, "sf_status": d.Status, "sf_version": d.Version}, nil); err != nil {
		return err
	}
	return a.installDefinition(ctx, d)
}

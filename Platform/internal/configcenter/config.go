package configcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Parameter struct {
	ID           string                 `json:"id"`
	Program      string                 `json:"program"`
	Category     string                 `json:"category"`
	Description  string                 `json:"description"`
	Schema       map[string]any         `json:"schema"`
	Value        any                    `json:"value"`
	Dynamic      bool                   `json:"dynamic"`
	Secret       bool                   `json:"secret"`
	Version      int64                  `json:"version"`
	Effective    map[string]int64       `json:"effective"`
	State        string                 `json:"state"`
	Applications map[string]Application `json:"applications,omitempty"`
}
type Application struct {
	Version int64  `json:"version"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	AtMS    int64  `json:"at_ms"`
}
type Service struct {
	Store    *store.Store
	Identity *identity.Manager
}

func Validate(schema map[string]any, value any) error {
	// Normalize programmatic Go values and HTTP JSON into the same schema types.
	b, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	var normalized map[string]any
	if err = store.DecodeJSON(b, &normalized); err != nil {
		return err
	}
	b, err = json.Marshal(value)
	if err != nil {
		return err
	}
	var data any
	if err = store.DecodeJSON(b, &data); err != nil {
		return err
	}
	return validate(normalized, data, "$", 0)
}
func validate(schema map[string]any, value any, path string, depth int) error {
	if depth > 32 {
		return errors.New("schema depth exceeds 32")
	}
	typ, _ := schema["type"].(string)
	switch typ {
	case "object":
		m, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		props, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, raw := range required {
			name, _ := raw.(string)
			if _, ok := m[name]; !ok {
				return fmt.Errorf("%s.%s is required", path, name)
			}
		}
		for name, v := range m {
			raw, ok := props[name]
			if !ok {
				if extra, ok := schema["additionalProperties"].(bool); ok && !extra {
					return fmt.Errorf("%s.%s is not allowed", path, name)
				}
				continue
			}
			child, ok := raw.(map[string]any)
			if !ok {
				return errors.New("invalid property schema")
			}
			if e := validate(child, v, path+"."+name, depth+1); e != nil {
				return e
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		if max, ok := store.Number(schema["maxItems"]); ok && int64(len(items)) > max.Num().Int64() {
			return fmt.Errorf("%s has too many items", path)
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for _, v := range items {
			if e := validate(itemSchema, v, path+"[]", depth+1); e != nil {
				return e
			}
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if max, ok := store.Number(schema["maxLength"]); ok && int64(len(s)) > max.Num().Int64() {
			return fmt.Errorf("%s is too long", path)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			r, e := regexp.Compile(pattern)
			if e != nil {
				return e
			}
			if !r.MatchString(s) {
				return fmt.Errorf("%s does not match its pattern", path)
			}
		}
	case "integer", "number":
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("%s must be %s", path, typ)
		}
		n, ok := store.Number(value)
		if !ok || typ == "integer" && !n.IsInt() {
			return fmt.Errorf("%s must be %s", path, typ)
		}
		if min, ok := store.Number(schema["minimum"]); ok && n.Cmp(min) < 0 {
			return fmt.Errorf("%s is below minimum", path)
		}
		if max, ok := store.Number(schema["maximum"]); ok && n.Cmp(max) > 0 {
			return fmt.Errorf("%s is above maximum", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be boolean", path)
		}
	case "null":
		if value != nil {
			return fmt.Errorf("%s must be null", path)
		}
	case "":
	default:
		return fmt.Errorf("unsupported schema type %s", typ)
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, v := range enum {
			if store.Hash(v) == store.Hash(value) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s is outside enum", path)
		}
	}
	return nil
}
func (s *Service) Put(ctx context.Context, actor model.Actor, p Parameter, expected int64) (Parameter, error) {
	if expected < 0 {
		return p, store.ErrConflict
	}
	if p.ID == "" || p.Program == "" || len(p.Schema) == 0 {
		return p, errors.New("id, program and schema are required")
	}
	if e := Validate(p.Schema, p.Value); e != nil {
		return p, e
	}
	if e := validateKnown(p); e != nil {
		return p, e
	}
	p.Version = expected + 1
	p.Effective = map[string]int64{}
	p.State = "pending"
	if !p.Dynamic {
		p.State = "restart_required"
	}
	if p.Secret {
		raw, e := json.Marshal(p.Value)
		if e != nil {
			return p, e
		}
		encrypted, e := s.Identity.Encrypt("config:"+p.ID, string(raw))
		if e != nil {
			return p, e
		}
		p.Value = map[string]any{"ciphertext": encrypted}
	}
	e := s.Store.Write(ctx, func(t *store.Tx) error {
		currentVersion := int64(0)
		doc, e := t.Get("parameter", p.ID)
		if e == nil {
			old, e := store.Decode[Parameter](doc)
			if e != nil {
				return e
			}
			if old.Version != expected {
				return store.ErrConflict
			}
			p.Effective = old.Effective
			p.Applications = old.Applications
			currentVersion = doc.Version
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		} else if expected != 0 {
			return store.ErrConflict
		}
		if _, e := t.Put("parameter", p.ID, currentVersion, p); e != nil {
			return e
		}
		public := p
		if public.Secret {
			public.Value = "********"
		}
		if e := t.Enqueue(fmt.Sprintf("config:%s:%d", p.ID, p.Version), "config_update", p.Program, public); e != nil {
			return e
		}
		return t.Audit(actor, "config.update", p.ID, "", public)
	})
	out := p
	if out.Secret {
		out.Value = "********"
	}
	return out, e
}
func (s *Service) List(ctx context.Context) ([]Parameter, error) {
	docs, e := s.Store.List(ctx, "parameter")
	if e != nil {
		return nil, e
	}
	out := []Parameter{}
	for _, d := range docs {
		p, e := store.Decode[Parameter](d)
		if e != nil {
			return nil, e
		}
		if p.Secret {
			p.Value = "********"
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Program == out[j].Program {
			return out[i].ID < out[j].ID
		}
		return out[i].Program < out[j].Program
	})
	return out, nil
}
func (s *Service) Value(ctx context.Context, id string) (any, error) {
	d, e := s.Store.Get(ctx, "parameter", id)
	if e != nil {
		return nil, e
	}
	p, e := store.Decode[Parameter](d)
	if e != nil {
		return nil, e
	}
	if !p.Secret {
		return p.Value, nil
	}
	m, ok := p.Value.(map[string]any)
	if !ok {
		return nil, errors.New("invalid encrypted parameter")
	}
	cipher, _ := m["ciphertext"].(string)
	plain, e := s.Identity.Decrypt("config:"+id, cipher)
	if e != nil {
		return nil, e
	}
	var v any
	e = store.DecodeJSON([]byte(plain), &v)
	return v, e
}
func (s *Service) Acknowledge(ctx context.Context, node, id string, version int64, applied bool, reason string) error {
	return s.Store.Write(ctx, func(t *store.Tx) error {
		d, e := t.Get("parameter", id)
		if e != nil {
			return e
		}
		p, e := store.Decode[Parameter](d)
		if e != nil {
			return e
		}
		if p.Version != version {
			return store.ErrConflict
		}
		if p.Effective == nil {
			p.Effective = map[string]int64{}
		}
		if applied {
			p.Effective[node] = version
			p.State = "applied"
		} else {
			p.State = "failed"
			if reason == "restart_required" {
				p.State = "restart_required"
			}
		}
		if p.Applications == nil {
			p.Applications = map[string]Application{}
		}
		p.Applications[node] = Application{Version: version, State: p.State, Reason: reason, AtMS: s.Store.Now().UnixMilli()}
		p.State = "applied"
		for _, application := range p.Applications {
			state := application.State
			if application.Version != p.Version {
				state = "pending"
			}
			if state == "failed" || p.State != "failed" && state == "restart_required" || p.State == "applied" && state != "applied" {
				p.State = state
			}
		}
		if _, e = t.Put("parameter", id, d.Version, p); e != nil {
			return e
		}
		if p.State == "applied" {
			rows, e := t.QueryContext(ctx, "SELECT id,payload FROM outbox WHERE kind='config_update'")
			if e != nil {
				return e
			}
			completed := []string{}
			for rows.Next() {
				var deliveryID, raw string
				if e = rows.Scan(&deliveryID, &raw); e != nil {
					rows.Close()
					return e
				}
				var queued Parameter
				if e = store.DecodeJSON([]byte(raw), &queued); e != nil {
					rows.Close()
					return e
				}
				if queued.ID == p.ID && queued.Version <= p.Version {
					completed = append(completed, deliveryID)
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, deliveryID := range completed {
				if _, e = t.ExecContext(ctx, "DELETE FROM outbox WHERE id=$1", deliveryID); e != nil {
					return e
				}
			}
		}
		return t.Audit(model.Actor{UserID: node, Source: "program"}, "config.acknowledge", id, "", map[string]any{"version": version, "applied": applied, "reason": reason})
	})
}
func Defaults() []Parameter {
	return []Parameter{
		{ID: "queue.watermarks", Program: "gateway", Category: "reliability", Description: "队列提醒、警告、错误与恢复水位", Schema: map[string]any{"type": "object", "required": []any{"reminder", "warning", "error", "recovery"}, "additionalProperties": false, "properties": map[string]any{"reminder": map[string]any{"type": "number", "minimum": 0, "maximum": 100}, "warning": map[string]any{"type": "number", "minimum": 0, "maximum": 100}, "error": map[string]any{"type": "number", "minimum": 0, "maximum": 100}, "recovery": map[string]any{"type": "number", "minimum": 0, "maximum": 100}}}, Value: map[string]any{"reminder": 60, "warning": 80, "error": 95, "recovery": 50}, Dynamic: true},
		{ID: "heartbeat.interval_ms", Program: "edge", Category: "reliability", Description: "边缘心跳周期", Schema: map[string]any{"type": "integer", "minimum": 1000, "maximum": 60000}, Value: 5000, Dynamic: true},
		{ID: "heartbeat.offline_ms", Program: "gateway", Category: "reliability", Description: "无心跳离线判定", Schema: map[string]any{"type": "integer", "minimum": 5000, "maximum": 300000}, Value: 15000, Dynamic: true},
		{ID: "control.approval_ttl_ms", Program: "cloud", Category: "control", Description: "人工确认有效期", Schema: map[string]any{"type": "integer", "minimum": 10000, "maximum": 3600000}, Value: 300000, Dynamic: true},
		{ID: "control.start_ttl_ms", Program: "cloud", Category: "control", Description: "正式下发后的启动期限", Schema: map[string]any{"type": "integer", "minimum": 1000, "maximum": 60000}, Value: 10000, Dynamic: true},
		{ID: "identity.offline_ttl_ms", Program: "cloud", Category: "identity", Description: "离线权限包有效期", Schema: map[string]any{"type": "integer", "minimum": 86400000, "maximum": 604800000}, Value: 172800000, Dynamic: true},
		{ID: "ui.refresh_ms", Program: "frontend", Category: "display", Description: "实时页面刷新周期", Schema: map[string]any{"type": "integer", "minimum": 250, "maximum": 60000}, Value: 1000, Dynamic: true},
	}
}

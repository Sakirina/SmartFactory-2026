package configcenter

import (
	"competition2026/product/platform/internal/store"
	"context"
	"encoding/json"
	"errors"
)

func validateKnown(p Parameter) error {
	if p.ID == "control.confirmations" {
		raw, _ := json.Marshal(p.Value)
		var v store.Confirmations
		if e := json.Unmarshal(raw, &v); e != nil {
			return e
		}
		if v.Engineers < 1 || v.Leaders < 1 || v.ForceEngineers < v.Engineers || v.ForceLeaders < v.Leaders {
			return errors.New("confirmation counts must be positive; forced execution requires at least the ordinary counts")
		}
	}
	if p.ID == "queue.watermarks" {
		raw, _ := json.Marshal(p.Value)
		var v map[string]int64
		if e := json.Unmarshal(raw, &v); e != nil {
			return e
		}
		if !(v["recovery"] < v["reminder"] && v["reminder"] < v["warning"] && v["warning"] < v["error"]) {
			return errors.New("queue watermarks require recovery < reminder < warning < error")
		}
	}
	if p.ID == "storage.retention" {
		raw, _ := json.Marshal(p.Value)
		var v store.Retention
		if e := json.Unmarshal(raw, &v); e != nil {
			return e
		}
		if v.RawDays > v.MinuteDays || v.MinuteDays > v.HourDays || v.HourDays > v.DayDays {
			return errors.New("retention periods must increase with aggregation granularity")
		}
	}
	return nil
}
func (s *Service) ApplyPolicy(ctx context.Context) error {
	p := store.DefaultPolicy()
	values := map[string]*int64{"storage.auto_backfill_days": &p.AutoBackfillDays, "heartbeat.interval_ms": &p.HeartbeatMS, "heartbeat.offline_ms": &p.OfflineMS, "control.approval_ttl_ms": &p.ApprovalTTLMS, "control.start_ttl_ms": &p.StartTTLMS, "identity.offline_ttl_ms": &p.PermissionTTLMS, "ui.refresh_ms": &p.RefreshMS, "queue.capacity": &p.QueueCapacity}
	for id, target := range values {
		value, e := s.Value(ctx, id)
		if errors.Is(e, store.ErrNotFound) {
			continue
		}
		if e != nil {
			return e
		}
		n, ok := store.Number(value)
		if !ok || !n.IsInt() || !n.Num().IsInt64() {
			return errors.New("configuration requires an integer")
		}
		*target = n.Num().Int64()
	}
	if value, e := s.Value(ctx, "queue.watermarks"); e == nil {
		raw, _ := json.Marshal(value)
		var v map[string]int64
		if e = json.Unmarshal(raw, &v); e != nil {
			return e
		}
		p.Reminder, p.Warning, p.Error, p.Recovery = v["reminder"], v["warning"], v["error"], v["recovery"]
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	if value, e := s.Value(ctx, "storage.retention"); e == nil {
		raw, _ := json.Marshal(value)
		if e = json.Unmarshal(raw, &p.Retention); e != nil {
			return e
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	if value, e := s.Value(ctx, "control.confirmations"); e == nil {
		raw, _ := json.Marshal(value)
		if e = json.Unmarshal(raw, &p.Confirmations); e != nil {
			return e
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	if value, e := s.Value(ctx, "storage.archive"); e == nil {
		raw, _ := json.Marshal(value)
		if e = json.Unmarshal(raw, &p.Archive); e != nil {
			return e
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	s.Store.SetPolicy(p)
	return nil
}
func AdditionalDefaults() []Parameter {
	integer := func(min, max int) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "maximum": max}
	}
	retentionSchema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"raw_days", "minute_days", "hour_days", "day_days", "alarm_days", "quarantine_days"}, "properties": map[string]any{"raw_days": integer(1, 3650), "minute_days": integer(1, 3650), "hour_days": integer(1, 3650), "day_days": integer(1, 36500), "alarm_days": integer(1, 36500), "quarantine_days": integer(1, 365)}}
	channelProperties := map[string]any{"enabled": map[string]any{"type": "boolean"}, "timeout_ms": integer(1000, 60000)}
	for _, key := range []string{"endpoint", "token", "smtp_address", "smtp_server_name", "smtp_tls_mode", "username", "password", "from"} {
		channelProperties[key] = map[string]any{"type": "string", "maxLength": 16384}
	}
	channelSchema := map[string]any{"type": "object", "required": []string{"enabled"}, "additionalProperties": false, "properties": channelProperties}
	return []Parameter{
		{ID: "storage.archive", Program: "platform", Category: "storage", Description: "完整观测的压缩归档、热数据小时数及每轮归档规模", Schema: map[string]any{"type": "object", "required": []string{"enabled", "hot_hours", "block_points", "blocks_per_run"}, "additionalProperties": false, "properties": map[string]any{"enabled": map[string]any{"type": "boolean"}, "hot_hours": integer(1, 87600), "block_points": integer(128, 4096), "blocks_per_run": integer(1, 64)}}, Value: store.DefaultArchivePolicy(), Dynamic: true},
		{ID: "storage.auto_backfill_days", Program: "platform", Category: "storage", Description: "自动补传的最大天数，超期数据转入人工检查与导入", Schema: integer(1, 3650), Value: 30, Dynamic: true},
		{ID: "control.confirmations", Program: "platform", Category: "control", Description: "普通控制和强制执行的工程师、领导会签人数", Schema: map[string]any{"type": "object", "required": []string{"engineers", "leaders", "force_engineers", "force_leaders"}, "additionalProperties": false, "properties": map[string]any{"engineers": integer(1, 8), "leaders": integer(1, 8), "force_engineers": integer(1, 8), "force_leaders": integer(1, 8)}}, Value: store.DefaultPolicy().Confirmations, Dynamic: true},
		{ID: "notification.email", Program: "cloud", Category: "notification", Description: "邮件渠道的 SMTP 地址、TLS 方式、发件人与凭据", Schema: channelSchema, Value: map[string]any{"enabled": false, "timeout_ms": 10000, "smtp_tls_mode": "starttls"}, Dynamic: true, Secret: true},
		{ID: "notification.sms", Program: "cloud", Category: "notification", Description: "短信服务 HTTP 地址、凭据与确认超时", Schema: channelSchema, Value: map[string]any{"enabled": false, "timeout_ms": 10000}, Dynamic: true, Secret: true},
		{ID: "storage.retention", Program: "platform", Category: "storage", Description: "原始观测、汇总、告警和超期补传的保留天数", Schema: retentionSchema, Value: store.DefaultRetention(), Dynamic: true},
		{ID: "queue.capacity", Program: "gateway", Category: "reliability", Description: "运行队列水位计算容量", Schema: integer(1000, 100000000), Value: 200000, Dynamic: true},
		{ID: "ai.model", Program: "cloud", Category: "assistant", Description: "页面助手的模型端点、名称、凭据和调用超时", Schema: map[string]any{"type": "object", "required": []string{"endpoint", "model", "api_key", "timeout_ms"}, "additionalProperties": false, "properties": map[string]any{"endpoint": map[string]any{"type": "string", "maxLength": 2048}, "model": map[string]any{"type": "string", "maxLength": 200}, "api_key": map[string]any{"type": "string", "maxLength": 16384}, "timeout_ms": integer(1000, 300000)}}, Value: map[string]any{"endpoint": "", "model": "", "api_key": "", "timeout_ms": 60000}, Secret: true, Dynamic: true},
	}
}

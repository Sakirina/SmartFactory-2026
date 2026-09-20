// Package aimock provides a deterministic Chat Completions simulator for acceptance.
package aimock

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"competition2026/product/platform/internal/ai"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Server struct{}

func (Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Messages []ai.Message `json:"messages"`
		Stream   bool         `json:"stream"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	user := ""
	outputs := []json.RawMessage{}
	for _, m := range req.Messages {
		if m.Role == "user" {
			user = m.Content
			outputs = nil
		}
		if m.Role == "tool" {
			outputs = append(outputs, json.RawMessage(m.Content))
		}
	}
	if strings.Contains(user, "模拟超时") {
		<-r.Context().Done()
		return
	}
	if strings.Contains(user, "模拟服务错误") {
		http.Error(w, "simulated upstream outage", 503)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(delta any, finish any) {
		b, _ := json.Marshal(map[string]any{"id": "sf-model-simulation", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\n", b)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	text := func(value string) {
		runes := []rune(value)
		for _, part := range []string{string(runes[:len(runes)/2]), string(runes[len(runes)/2:])} {
			emit(map[string]string{"content": part}, nil)
		}
		emit(map[string]any{}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
	call := func(name string, args any) {
		b, _ := json.Marshal(args)
		id := fmt.Sprintf("simulation_call_%d", len(outputs))
		cut := len(b) / 2
		emit(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]string{"name": name, "arguments": string(b[:cut])}}}}, nil)
		emit(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]string{"arguments": string(b[cut:])}}}}, nil)
		emit(map[string]any{}, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
	if strings.Contains(user, "直接发布") || strings.Contains(user, "读取密钥") || strings.Contains(user, "直接控制") {
		if len(outputs) == 0 {
			tool := "publish_definition"
			if strings.Contains(user, "读取密钥") {
				tool = "read_secret"
			}
			if strings.Contains(user, "直接控制") {
				tool = "control_device"
			}
			call(tool, map[string]string{"id": "ventilation-plan"})
		} else {
			text("模拟模型已尝试越权工具，平台拒绝了请求，正式版本保持有效。")
		}
		return
	}
	if len(outputs) > 0 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(outputs[len(outputs)-1], &failure) == nil && failure.Error != "" {
			text("平台工具未完成操作：" + failure.Error)
			return
		}
	}
	if match := regexp.MustCompile(`修改草稿\s+([A-Za-z0-9_.:-]+)\s+名称=(.+)`).FindStringSubmatch(user); len(match) == 3 {
		draftID, name := match[1], strings.TrimSpace(match[2])
		switch len(outputs) {
		case 0:
			call("list_drafts", map[string]any{})
		case 1:
			var drafts []model.Draft
			if store.DecodeJSON(outputs[0], &drafts) != nil {
				text("草稿列表读取失败。")
				return
			}
			for _, draft := range drafts {
				if draft.ID == draftID {
					draft.Definition.Name = name
					call("save_draft", map[string]any{"draft": draft, "expected_version": draft.Version})
					return
				}
			}
			text("当前授权范围没有指定草稿。")
		case 2:
			call("validate_draft", map[string]string{"id": draftID})
		case 3:
			call("diff_draft", map[string]string{"id": draftID})
		default:
			text("模拟草稿已修改，并已执行校验与差异比较；草稿标识：" + draftID)
		}
		return
	}
	kind := "analysis"
	if strings.Contains(user, "告警") || strings.Contains(user, "alarm") {
		kind = "alarm"
	}
	if strings.Contains(user, "策略") || strings.Contains(user, "strategy") {
		kind = "strategy"
	}
	h := sha256.Sum256([]byte(user))
	draftID := "ai-" + kind + "-" + hex.EncodeToString(h[:5])
	if strings.Contains(user, "草稿") || strings.Contains(user, "draft") {
		switch len(outputs) {
		case 0:
			call("list_definitions", map[string]string{"kind": kind})
		case 1:
			var defs []model.Definition
			if store.DecodeJSON(outputs[0], &defs) != nil || len(defs) == 0 {
				text("当前授权范围没有可供模拟使用的同类定义。")
				return
			}
			call("get_definition", map[string]string{"id": defs[0].ID})
		case 2:
			var d model.Definition
			if store.DecodeJSON(outputs[1], &d) != nil || d.ID == "" {
				text("读取定义失败。")
				return
			}
			d.ID = draftID
			d.Name = "AI 模拟草稿 · " + d.Name
			d.Status = "draft"
			d.Version = 0
			d.EffectiveMS = 0
			call("save_draft", map[string]any{"draft": model.Draft{ID: draftID, Definition: d}, "expected_version": 0})
		case 3:
			call("validate_draft", map[string]string{"id": draftID})
		case 4:
			call("diff_draft", map[string]string{"id": draftID})
		default:
			text("模拟草稿已保存，并已执行校验与差异比较；请在编排页面审阅具体参数后发布。草稿标识：" + draftID)
		}
		return
	}
	switch len(outputs) {
	case 0:
		call("discover_catalogue", map[string]any{})
	case 1:
		var catalogue struct {
			Entries []struct {
				ID string `json:"id"`
			} `json:"entries"`
		}
		if store.DecodeJSON(outputs[0], &catalogue) != nil || len(catalogue.Entries) == 0 {
			text("当前授权范围没有已发布的数据输出。")
			return
		}
		call("describe_output", map[string]string{"id": catalogue.Entries[0].ID})
	case 2:
		var description struct {
			ID       string         `json:"id"`
			Selector model.Selector `json:"selector"`
		}
		if store.DecodeJSON(outputs[1], &description) != nil {
			text("输出结构读取失败。")
			return
		}
		ids := description.Selector.DeviceIDs
		if description.Selector.AssetID != "" {
			ids = []string{description.Selector.AssetID}
		}
		call("query_data", map[string]any{"device_ids": strings.Join(ids, ","), "keys": description.ID, "from_ms": time.Now().Add(-time.Hour).UnixMilli(), "to_ms": time.Now().UnixMilli(), "limit": 100})
	default:
		var result model.DataResult
		if store.DecodeJSON(outputs[len(outputs)-1], &result) != nil {
			text("模拟查询返回错误。")
			return
		}
		text(fmt.Sprintf("模拟查询完成：取得 %d 条观测，GOOD 数据 %d 条，排除 %d 条；完整程度为 %s。具体来源状态与修订记录可在数据监控页面查看。", len(result.Points), result.Quality.Good, result.Quality.Excluded, result.Quality.Completeness))
	}
}

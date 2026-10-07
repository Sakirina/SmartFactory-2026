// Package aievidencefixture implements a local model for full HTTP acceptance.
package aievidencefixture

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"competition2026/product/platform/internal/ai"
	"competition2026/product/platform/internal/store"
)

type Plan struct {
	DefinitionID, DeviceID, ExecutionID, RunID, OversizedID string
	FromMS, ToMS                                            int64
}

type Model struct {
	Plan      Plan
	OnOutputs func([]ai.Message)
	mu        sync.Mutex
	previous  string
}

func (m *Model) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || (r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/v1/responses") {
		http.NotFound(w, r)
		return
	}
	var request struct {
		Messages []ai.Message      `json:"messages"`
		Input    []json.RawMessage `json:"input"`
		Stream   bool              `json:"stream"`
	}
	if err := store.DecodeJSONReader(http.MaxBytesReader(w, r.Body, 4<<20), &request); err != nil {
		http.Error(w, "invalid model request", 400)
		return
	}
	responses := r.URL.Path == "/v1/responses"
	if responses {
		for _, raw := range request.Input {
			var input struct {
				Role    string          `json:"role"`
				Type    string          `json:"type"`
				CallID  string          `json:"call_id"`
				Output  string          `json:"output"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(raw, &input) != nil {
				continue
			}
			if input.Type == "function_call_output" {
				request.Messages = append(request.Messages, ai.Message{Role: "tool", ToolCallID: input.CallID, Content: input.Output})
				continue
			}
			var text string
			if json.Unmarshal(input.Content, &text) != nil {
				var parts []struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(input.Content, &parts)
				for _, part := range parts {
					text += part.Text
				}
			}
			request.Messages = append(request.Messages, ai.Message{Role: input.Role, Content: text})
		}
	}
	user := ""
	outputs := []ai.Message{}
	for _, item := range request.Messages {
		if item.Role == "user" {
			user = item.Content
			outputs = nil
		}
		if item.Role == "tool" {
			outputs = append(outputs, item)
		}
	}
	if m.OnOutputs != nil {
		m.OnOutputs(outputs)
	}
	if strings.Contains(user, "模拟超时") {
		<-r.Context().Done()
		return
	}
	if strings.Contains(user, "模拟服务错误") {
		http.Error(w, "local model unavailable", 503)
		return
	}
	if strings.Contains(user, "模拟中断") && len(outputs) > 0 {
		w.Header().Set("Content-Type", "text/event-stream")
		if responses {
			emitResponse(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": "message-final", "delta": "生成尚未完成"})
		} else {
			emitChat(w, map[string]any{"content": "生成尚未完成"}, nil)
		}
		return
	}
	text := ""
	if strings.Contains(user, "无依据") {
		text = "本次回答尚未取得工具依据。"
	}
	if strings.Contains(user, "伪造引用") {
		text = "模拟未知引用 [evidence:unknown-fixture-reference]"
	}
	if strings.Contains(user, "跨调查") {
		m.mu.Lock()
		previous := m.previous
		m.mu.Unlock()
		if previous == "" {
			previous = "unknown-previous-investigation"
		}
		text = "模拟其他调查引用 [evidence:" + previous + "]"
	}
	name := ""
	var args any
	if text == "" {
		if strings.Contains(user, "仅查询") {
			if len(outputs) == 0 {
				name = "query_data"
				args = map[string]any{"device_ids": m.Plan.DeviceID, "keys": "temperature", "from_ms": m.Plan.FromMS, "to_ms": m.Plan.ToMS, "limit": 20}
			}
		} else if strings.Contains(user, "空查询") || strings.Contains(user, "查询失败") {
			if len(outputs) == 0 {
				name = "query_data"
				device := m.Plan.DeviceID
				if strings.Contains(user, "查询失败") {
					device = "private-device"
				}
				args = map[string]any{"device_ids": device, "keys": "temperature", "from_ms": 1, "to_ms": 2, "limit": 20}
			}
		} else if strings.Contains(user, "超预算") {
			if len(outputs) == 0 {
				name = "get_definition"
				args = map[string]any{"id": m.Plan.OversizedID}
			}
		} else if strings.Contains(user, "读取身份") {
			id := strings.TrimSpace(strings.SplitN(user, "读取身份", 2)[1])
			if len(outputs) == 0 {
				name = "get_definition"
				args = map[string]any{"id": id}
			}
		} else {
			steps := []struct {
				name string
				args any
			}{
				{"get_definition", map[string]any{"id": m.Plan.DefinitionID}},
				{"query_data", map[string]any{"device_ids": m.Plan.DeviceID, "keys": "temperature", "from_ms": m.Plan.FromMS, "to_ms": m.Plan.ToMS, "limit": 20}},
			}
			if m.Plan.ExecutionID != "" {
				steps = append(steps, struct {
					name string
					args any
				}{"get_execution", map[string]any{"id": m.Plan.ExecutionID}})
			}
			if m.Plan.RunID != "" {
				steps = append(steps, struct {
					name string
					args any
				}{"get_analysis_run", map[string]any{"id": m.Plan.RunID}}, struct {
					name string
					args any
				}{"get_analysis_steps", map[string]any{"id": m.Plan.RunID, "limit": 20}})
			}
			if len(outputs) < len(steps) {
				name = steps[len(outputs)].name
				args = steps[len(outputs)].args
			}
		}
	}
	if name != "" {
		writeCall(w, responses, request.Stream, len(outputs), name, args)
		return
	}
	if text == "" {
		text = "本地模型已完成调查，返回内容包含查询范围、质量、版本及分页状态。"
		for _, output := range outputs {
			var item struct {
				Evidence struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"_evidence"`
			}
			if json.Unmarshal([]byte(output.Content), &item) == nil && item.Evidence.ID != "" {
				text += "\n工具 " + output.ToolCallID + " 返回状态 " + item.Evidence.Status + " [evidence:" + item.Evidence.ID + "]"
				m.mu.Lock()
				m.previous = item.Evidence.ID
				m.mu.Unlock()
			}
		}
	}
	writeText(w, responses, request.Stream, text)
}

func emitResponse(w http.ResponseWriter, kind string, value any) {
	raw, _ := json.Marshal(value)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
}
func emitChat(w http.ResponseWriter, delta any, finish any) {
	raw, _ := json.Marshal(map[string]any{"id": "fixture-chat", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	fmt.Fprintf(w, "data: %s\n\n", raw)
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
}
func writeCall(w http.ResponseWriter, responses, stream bool, round int, name string, args any) {
	raw, _ := json.Marshal(args)
	arguments := string(raw)
	callID := fmt.Sprintf("fixture-call-%d", round)
	if responses {
		item := map[string]any{"id": fmt.Sprintf("function-%d", round), "type": "function_call", "status": "completed", "call_id": callID, "name": name, "arguments": arguments}
		response := map[string]any{"id": fmt.Sprintf("fixture-response-%d", round), "status": "completed", "output": []any{map[string]any{"id": fmt.Sprintf("reasoning-%d", round), "type": "reasoning", "summary": []any{}, "encrypted_content": "local-fixture-reasoning"}, item}}
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		added := map[string]any{}
		for k, v := range item {
			added[k] = v
		}
		added["status"] = "in_progress"
		added["arguments"] = ""
		emitResponse(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 1, "item": added})
		runes := []rune(arguments)
		for _, fragment := range []string{string(runes[:len(runes)/2]), string(runes[len(runes)/2:])} {
			emitResponse(w, "response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "item_id": item["id"], "output_index": 1, "delta": fragment})
		}
		emitResponse(w, "response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "item_id": item["id"], "output_index": 1, "arguments": arguments})
		emitResponse(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 1, "item": item})
		emitResponse(w, "response.completed", map[string]any{"type": "response.completed", "response": response})
		return
	}
	call := map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture-chat", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "tool_calls": []any{call}}, "finish_reason": "tool_calls"}}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	cut := len(raw) / 2
	emitChat(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": arguments[:cut]}}}}, nil)
	emitChat(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": arguments[cut:]}}}}, nil)
	emitChat(w, map[string]any{}, "tool_calls")
	fmt.Fprint(w, "data: [DONE]\n\n")
}
func writeText(w http.ResponseWriter, responses, stream bool, text string) {
	if responses {
		response := map[string]any{"id": "fixture-final", "status": "completed", "output": []any{map[string]any{"id": "message-final", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}}}
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emitResponse(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "item_id": "message-final", "delta": text})
		emitResponse(w, "response.completed", map[string]any{"type": "response.completed", "response": response})
		return
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture-chat", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	emitChat(w, map[string]any{"content": text}, nil)
	emitChat(w, map[string]any{}, "stop")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

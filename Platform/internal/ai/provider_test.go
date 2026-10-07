package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const responseTool = `{"id":"resp-one","status":"completed","output":[{"id":"reason","type":"reasoning","summary":[{"type":"summary_text","text":"检查数值"}],"encrypted_content":"opaque-test-value"},{"id":"function-one","type":"function_call","call_id":"call-one","name":"query_data","arguments":"{\"value\":9223372036854775807}"}],"usage":{"input_tokens":12,"output_tokens":4}}`
const responseText = `{"id":"resp-two","status":"completed","output":[{"id":"message-one","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"查询完成","annotations":[]}]}],"usage":{"input_tokens":20,"output_tokens":3}}`
const chatTool = `{"id":"chat-one","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-one","type":"function","function":{"name":"query_data","arguments":"{\"value\":9223372036854775807}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":4}}`
const chatText = `{"id":"chat-two","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"查询"},{"type":"text","text":"完成"}],"annotations":[]},"finish_reason":"stop"}]}`

func sseEvent(kind string, value any) string {
	raw, _ := json.Marshal(value)
	return "event: " + kind + "\r\ndata: " + string(raw) + "\r\n\r\n"
}
func responsesToolStream() string {
	stream := sseEvent("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"type": "function_call", "id": "function-one", "call_id": "call-one", "name": "query_data", "arguments": ""}})
	for _, fragment := range []string{`{"value":`, `9223372036854775807}`} {
		stream += sseEvent("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": 1, "item_id": "function-one", "delta": fragment})
	}
	stream += sseEvent("response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "output_index": 1, "item_id": "function-one", "arguments": `{"value":9223372036854775807}`})
	return stream + sseEvent("response.completed", map[string]any{"type": "response.completed", "response": json.RawMessage(responseTool)})
}
func responsesTextStream() string {
	return sseEvent("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "message-one", "output_index": 0, "delta": "查询"}) + sseEvent("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "message-one", "output_index": 0, "delta": "完成"}) + sseEvent("response.completed", map[string]any{"type": "response.completed", "response": json.RawMessage(responseText)})
}
func chatToolStream() string {
	return `data: {"id":"chat-one","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-one","type":"function","function":{"name":"query_data","arguments":"{\"value\":"}}]}}]}

data: {"id":"chat-one","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"9223372036854775807}"}}]},"finish_reason":"tool_calls"}]}

data: {"id":"chat-one","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4}}

data: [DONE]

`
}
func chatTextStream() string {
	return `data: {"id":"chat-two","choices":[{"index":0,"delta":{"content":"查询"}}]}

data: {"id":"chat-two","choices":[{"index":0,"delta":{"content":"完成"},"finish_reason":"stop"}]}

data: [DONE]

`
}

func TestModelAdaptersCompleteAndStreamUseSameToolRound(t *testing.T) {
	for _, api := range []string{"chat_completions", "responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", api, stream), func(t *testing.T) {
				requests := 0
				host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					expectedPath := "/v1/chat/completions"
					if api == "responses" {
						expectedPath = "/v1/responses"
					}
					if r.URL.Path != expectedPath {
						t.Errorf("path %s", r.URL.Path)
					}
					if r.Header.Get("Authorization") != "Bearer fixture-key" {
						t.Error("missing model credential")
					}
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					var body map[string]json.RawMessage
					if json.Unmarshal(raw, &body) != nil {
						t.Error("invalid request")
					}
					if string(body["stream"]) != fmt.Sprint(stream) || string(body["store"]) != "false" {
						t.Error(string(raw))
					}
					if api == "responses" {
						if len(body["messages"]) != 0 || !strings.Contains(string(body["tools"]), `"name":"query_data"`) {
							t.Error(string(raw))
						}
						if requests == 2 && (!strings.Contains(string(body["input"]), "opaque-test-value") || !strings.Contains(string(body["input"]), "function_call_output") || !strings.Contains(string(body["input"]), "call-one")) {
							t.Error("lost reasoning/function context", string(raw))
						}
					} else if len(body["input"]) != 0 || !strings.Contains(string(body["tools"]), `"function":`) {
						t.Error(string(raw))
					}
					output := chatTool
					if requests == 2 {
						output = chatText
					}
					if api == "responses" {
						output = responseTool
						if requests == 2 {
							output = responseText
						}
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						output = chatToolStream()
						if requests == 2 {
							output = chatTextStream()
						}
						if api == "responses" {
							output = responsesToolStream()
							if requests == 2 {
								output = responsesTextStream()
							}
						}
					} else {
						w.Header().Set("Content-Type", "application/json")
					}
					fmt.Fprint(w, output)
				}))
				defer host.Close()
				cfg := ModelConfig{Provider: "openai", API: api, Stream: &stream, Endpoint: host.URL + "/v1", Model: "fixture-model", APIKey: "fixture-key"}
				messages := []ModelMessage{textMessage("user", "查询温度")}
				tools := []Tool{{Name: "query_data", InputSchema: object(map[string]any{"value": map[string]any{"type": "integer"}})}}
				chat := &Chat{}
				events := []StreamEvent{}
				emit := func(event StreamEvent) error { events = append(events, event); return nil }
				first, err := chat.modelCompletion(context.Background(), cfg, messages, tools, emit)
				if err != nil || len(first.Message.ToolCalls) != 1 {
					t.Fatal(first, err)
				}
				call := first.Message.ToolCalls[0]
				if call.ID != "call-one" || call.Function.Arguments != `{"value":9223372036854775807}` {
					t.Fatal(call)
				}
				if len(first.Usage) == 0 || first.Status != "completed" {
					t.Fatal(first)
				}
				messages = append(messages, first.Message)
				result := textMessage("tool", `{"count":1}`)
				result.ToolCallID = call.ID
				messages = append(messages, result)
				second, err := chat.modelCompletion(context.Background(), cfg, messages, tools, emit)
				if err != nil || second.Message.Text() != "查询完成" || requests != 2 {
					t.Fatal(second, err, requests)
				}
				var text strings.Builder
				for _, event := range events {
					if event.Type == "text_delta" {
						text.WriteString(event.Text)
					}
				}
				if text.String() != "查询完成" {
					t.Fatal(text.String())
				}
			})
		}
	}
}
func TestResponsesIncompleteOrConflictingStreamsNeverYieldExecutableCalls(t *testing.T) {
	delta := sseEvent("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "item_id": "missing", "delta": "{}"})
	cases := map[string]string{
		"orphan fragment":   delta,
		"truncated":         strings.Split(responsesToolStream(), "event: response.completed")[0],
		"failed":            sseEvent("response.failed", map[string]any{"type": "response.failed", "response": map[string]any{"status": "failed", "error": map[string]string{"code": "server_error", "message": "upstream failed"}}}),
		"incomplete":        sseEvent("response.incomplete", map[string]any{"type": "response.incomplete", "response": map[string]any{"status": "incomplete", "incomplete_details": map[string]string{"reason": "max_output_tokens"}}}),
		"argument conflict": strings.Replace(responsesToolStream(), `\"value\":9223372036854775807`, `\"value\":9223372036854775806`, 1),
		"event mismatch":    "event: response.completed\ndata: {\"type\":\"error\",\"message\":\"failed\"}\n\n",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := (responsesAdapter{}).stream(strings.NewReader(value), func(StreamEvent) error { return nil })
			if err == nil || len(result.Message.ToolCalls) > 0 {
				t.Fatal(result, err)
			}
		})
	}
}
func TestModelMessageBlocksAndNonStreamingErrors(t *testing.T) {
	result, err := (chatCompletionsAdapter{}).complete([]byte(chatText))
	if err != nil || len(result.Message.Content) != 2 || result.Message.Text() != "查询完成" {
		t.Fatal(result, err)
	}
	result, err = (responsesAdapter{}).complete([]byte(responseTool))
	if err != nil || len(result.Message.NativeItems) != 2 || result.Message.Content[0].Type != "reasoning_summary" {
		t.Fatal(result, err)
	}
	for _, raw := range []string{strings.Replace(responseTool, `"completed"`, `"in_progress"`, 1), strings.Replace(responseTool, `{\"value\":9223372036854775807}`, `null`, 1), `{"status":"failed","error":{"message":"failure"}}`} {
		if _, err = (responsesAdapter{}).complete([]byte(raw)); err == nil {
			t.Fatal("invalid completed response accepted", raw)
		}
	}
}
func TestChatStreamRequiresTerminalAndCompleteArguments(t *testing.T) {
	for _, raw := range []string{strings.Replace(chatToolStream(), "data: [DONE]", "", 1), "data: [DONE]\n\n", strings.Replace(chatToolStream(), `"tool_calls"}]}`, `"length"}]}`, 1), strings.Replace(chatToolStream(), `9223372036854775807}`, `9223372036854775807`, 1)} {
		if _, err := (chatCompletionsAdapter{}).stream(strings.NewReader(raw), func(StreamEvent) error { return nil }); err == nil {
			t.Fatal("broken stream accepted", raw)
		}
	}
}
func TestModelErrorClassificationAndCancellation(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, "fixture-key should never be surfaced")
	}))
	defer host.Close()
	_, err := (&Chat{}).modelCompletion(context.Background(), ModelConfig{Endpoint: host.URL, Model: "test", APIKey: "fixture-key"}, []ModelMessage{textMessage("user", "hi")}, nil, nil)
	var modelErr *ModelError
	if !errors.As(err, &modelErr) || modelErr.HTTPStatus != 429 || !modelErr.Retryable || strings.Contains(err.Error(), "fixture-key") {
		t.Fatal(err)
	}
	if _, _, err = selectedAdapter(ModelConfig{Provider: "unknown", API: "responses"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, _, err = selectedAdapter(ModelConfig{Provider: "openai", API: "unknown"}); err == nil {
		t.Fatal("unknown API accepted")
	}
	stop := errors.New("caller cancelled")
	if _, err := (responsesAdapter{}).stream(strings.NewReader(responsesTextStream()), func(StreamEvent) error { return stop }); !errors.Is(err, stop) {
		t.Fatal(err)
	}
}
func TestSSEMultilineCRLFAndBudget(t *testing.T) {
	frames := 0
	err := readSSE(strings.NewReader(": heartbeat\r\nevent: sample\r\ndata: {\"ok\":\r\ndata: true}\r\n\r\n"), func(kind string, raw []byte) (bool, error) {
		frames++
		if kind != "sample" || !json.Valid(raw) {
			t.Fatal(kind, string(raw))
		}
		return true, nil
	})
	if err != nil || frames != 1 {
		t.Fatal(frames, err)
	}
	if _, err = readBounded(strings.NewReader("long"), 3); err == nil {
		t.Fatal("response budget ignored")
	}
}

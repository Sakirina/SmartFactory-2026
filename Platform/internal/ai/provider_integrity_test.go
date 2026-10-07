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
	"time"
)

func TestModelRequestsPreserveContentBlocks(t *testing.T) {
	completed := `{"id":"chat-blocks","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"first","annotations":[{"type":"citation","index":9007199254740993}]},{"type":"refusal","refusal":"second"}]} ,"finish_reason":"stop"}]}`
	response, err := (chatCompletionsAdapter{}).complete([]byte(completed))
	if err != nil {
		t.Fatal(err)
	}
	body, err := (chatCompletionsAdapter{}).request(ModelConfig{Model: "fixture"}, []ModelMessage{response.Message}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), `"annotations":[`) || !strings.Contains(string(raw), `"index":9007199254740993`) || !strings.Contains(string(raw), `"type":"citation"`) || !strings.Contains(string(raw), `"type":"refusal"`) || !strings.Contains(string(raw), `"refusal":"second"`) {
		t.Fatal("content blocks were flattened or rounded")
	}
	for _, api := range []string{"chat_completions", "responses"} {
		t.Run(api, func(t *testing.T) {
			kind := "image_url"
			image := json.RawMessage(`{"type":"image_url","image_url":{"url":"https://fixture.invalid/image.png","detail":"low"}}`)
			if api == "responses" {
				kind = "input_image"
				image = json.RawMessage(`{"type":"input_image","image_url":"https://fixture.invalid/image.png","detail":"low"}`)
			}
			message := ModelMessage{Role: "user", Content: []ContentBlock{{Type: "text", Text: "inspect"}, {Type: kind, Raw: image}}}
			adapter, cfg, err := selectedAdapter(ModelConfig{Provider: "openai-compatible", API: api, Model: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			body, err := adapter.request(cfg, []ModelMessage{message}, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(body)
			if !strings.Contains(string(raw), "fixture.invalid/image.png") || !strings.Contains(string(raw), `"detail":"low"`) || !strings.Contains(string(raw), "inspect") {
				t.Fatal("lost non-text message payload")
			}
		})
	}
}

func TestResponsesInterleavedContentBlocksRetainItemAndContentIndexes(t *testing.T) {
	completed := `{"id":"multi-content","status":"completed","output":[{"type":"message","id":"message-zero","role":"assistant","status":"completed","content":[{"type":"output_text","text":"first-end","annotations":[]},{"type":"refusal","refusal":"refused"}]},{"type":"message","id":"message-one","role":"assistant","status":"completed","content":[{"type":"output_text","text":"second","annotations":[]}]}]}`
	stream := ""
	for _, delta := range []struct {
		output, content  int
		item, kind, text string
	}{
		{0, 0, "message-zero", "response.output_text.delta", "first"}, {1, 0, "message-one", "response.output_text.delta", "second"}, {0, 1, "message-zero", "response.refusal.delta", "refused"}, {0, 0, "message-zero", "response.output_text.delta", "-end"},
	} {
		stream += sseEvent(delta.kind, map[string]any{"type": delta.kind, "output_index": delta.output, "content_index": delta.content, "item_id": delta.item, "delta": delta.text})
	}
	stream += sseEvent("response.completed", map[string]any{"type": "response.completed", "response": json.RawMessage(completed)})
	events := []StreamEvent{}
	response, err := (responsesAdapter{}).stream(strings.NewReader(stream), func(event StreamEvent) error { events = append(events, event); return nil })
	if err != nil || response.Message.Text() != "first-endrefusedsecond" || len(response.Message.Content) != 3 || len(events) != 4 || events[2].ContentIndex != 1 {
		t.Fatal("interleaved content was corrupted", err)
	}
	for _, broken := range []string{strings.Replace(stream, `"item_id":"message-zero"`, `"item_id":"different-message"`, 1), strings.Replace(stream, `"text":"first-end"`, `"text":"changed"`, 1)} {
		if _, err := (responsesAdapter{}).stream(strings.NewReader(broken), func(StreamEvent) error { return nil }); err == nil {
			t.Fatal("changed final content or identity accepted")
		}
	}
	wrongResponse := sseEvent("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": "different-response", "status": "in_progress"}}) + stream
	if _, err := (responsesAdapter{}).stream(strings.NewReader(wrongResponse), func(StreamEvent) error { return nil }); err == nil {
		t.Fatal("changed response id accepted")
	}
}

func TestModelToolArgumentsRejectDuplicateKeysAndNestedConflicts(t *testing.T) {
	for _, raw := range []string{`{"expected_version":0,"expected_version":1}`, `{"draft":{"id":"one","id":"two"}}`, `{"draft":{"params":[{"value":0,"value":1}]}}`, `{} {}`, `null`, `[]`} {
		if err := validateToolArguments(raw); err == nil {
			t.Fatal("ambiguous or non-object arguments accepted")
		}
	}
	if err := validateToolArguments(`{"value":9223372036854775807,"nested":[true,{"same":"first"},{"same":"second"}]}`); err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(responseTool, `{\"value\":9223372036854775807}`, `{\"value\":9223372036854775807,\"value\":0}`, 1)
	if _, err := (responsesAdapter{}).complete([]byte(duplicate)); err == nil {
		t.Fatal("duplicate arguments yielded executable calls")
	}
}

func TestModelResponsesRejectMalformedContentAndContradictoryFinish(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(chatTool, `"finish_reason":"tool_calls"`, `"finish_reason":"stop"`, 1),
		strings.Replace(chatText, `"finish_reason":"stop"`, `"finish_reason":"tool_calls"`, 1),
		strings.Replace(chatText, `"type":"text"`, `"unexpected":"text"`, 1),
	} {
		if _, err := (chatCompletionsAdapter{}).complete([]byte(raw)); err == nil {
			t.Fatal("contradictory completed message accepted")
		}
	}
	for _, raw := range []string{
		strings.Replace(chatToolStream(), `"finish_reason":"tool_calls"`, `"finish_reason":"stop"`, 1),
		strings.Replace(chatTextStream(), `"finish_reason":"stop"`, `"finish_reason":"tool_calls"`, 1),
	} {
		if _, err := (chatCompletionsAdapter{}).stream(strings.NewReader(raw), func(StreamEvent) error { return nil }); err == nil {
			t.Fatal("contradictory stream finish accepted")
		}
	}
	if _, err := (responsesAdapter{}).complete([]byte(`{"status":"completed","output":[{}]}`)); err == nil {
		t.Fatal("missing native output type accepted")
	}
	parts := make([]json.RawMessage, 129)
	for index := range parts {
		parts[index] = json.RawMessage(`{"type":"text","text":"fixture"}`)
	}
	raw, _ := json.Marshal(parts)
	if _, err := parseBlocks(raw); err == nil {
		t.Fatal("content block count budget ignored")
	}
}

func TestModelBudgetsRejectOverlongStreamsResponsesAndArguments(t *testing.T) {
	for _, sample := range []struct{ name, value string }{
		{"stream bytes", strings.Repeat(":"+strings.Repeat("h", 1022)+"\n", 8300) + "data: terminal\n\n"},
		{"stream line", "data: " + strings.Repeat("x", 1<<20) + "\n\n"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			if err := readSSE(strings.NewReader(sample.value), func(string, []byte) (bool, error) { return true, nil }); err == nil {
				t.Fatal("SSE budget ignored")
			}
		})
	}
	if _, err := readBounded(strings.NewReader(strings.Repeat("x", (8<<20)+1)), 8<<20); err == nil {
		t.Fatal("response byte budget ignored")
	}
	largeText := strings.Replace(responseText, "查询完成", strings.Repeat("x", (1<<20)+1), 1)
	if _, err := (responsesAdapter{}).complete([]byte(largeText)); err == nil {
		t.Fatal("response text budget ignored")
	}
	items := make([]json.RawMessage, 129)
	for index := range items {
		items[index] = json.RawMessage(`{"type":"reasoning","summary":[]}`)
	}
	raw, _ := json.Marshal(map[string]any{"status": "completed", "output": items})
	if _, err := (responsesAdapter{}).complete(raw); err == nil {
		t.Fatal("output item budget ignored")
	}
	argument := "{\"value\":\"" + strings.Repeat("x", 256<<10) + "\"}"
	call := ToolCall{ID: "call", Type: "function"}
	call.Function.Name = "save_draft"
	call.Function.Arguments = argument
	if err := validCalls([]ToolCall{call}); err == nil {
		t.Fatal("tool argument budget ignored")
	}
	call.Function.Arguments = "{}"
	calls := make([]ToolCall, 17)
	for index := range calls {
		calls[index] = call
		calls[index].ID = fmt.Sprint(index)
	}
	if err := validCalls(calls); err == nil {
		t.Fatal("tool count budget ignored")
	}
	for _, api := range []string{"chat_completions", "responses"} {
		stream := false
		_, err := (&Chat{}).modelCompletion(context.Background(), ModelConfig{API: api, Stream: &stream, Model: "fixture", Endpoint: "http://127.0.0.1:9"}, []ModelMessage{textMessage("user", strings.Repeat("x", (4<<20)+1))}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "context budget") {
			t.Fatal("request budget ignored", err)
		}
	}
}

func TestModelHTTPStreamCancellationAndDeadlines(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			closed := make(chan struct{})
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, sseEvent("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "partial", "output_index": 0, "delta": "partial"}))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(closed)
			}))
			defer host.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			_, err := (&Chat{}).modelCompletion(ctx, ModelConfig{Provider: "openai", API: "responses", Endpoint: host.URL, Model: "fixture"}, []ModelMessage{textMessage("user", "query")}, nil, func(event StreamEvent) error {
				if mode == "cancel" && event.Type == "text_delta" {
					cancel()
				}
				return nil
			})
			var failure *ModelError
			want := context.DeadlineExceeded
			code := "timeout"
			if mode == "cancel" {
				want = context.Canceled
				code = "cancelled"
			}
			if !errors.As(err, &failure) || !errors.Is(err, want) || failure.Code != code || failure.Retryable {
				t.Fatal("cancellation was not classified", err)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("cancelled model connection remained open")
			}
		})
	}
}

func TestModelCancellationRejectsAlreadyBufferedSuccessfulCompletion(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, responsesToolStream())
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, responseTool)
				}
			}))
			defer host.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			response, err := (&Chat{}).modelCompletion(ctx, ModelConfig{API: "responses", Stream: &stream, Endpoint: host.URL, Model: "fixture"}, nil, nil, func(event StreamEvent) error {
				if event.Type == "tool_call_start" || event.Type == "completed" {
					cancel()
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) || len(response.Message.ToolCalls) != 0 {
				t.Fatal("cancelled buffered response yielded executable calls", err)
			}
		})
	}
}

func TestModelHTTPErrorsAndResponseBudget(t *testing.T) {
	for _, status := range []int{401, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "private-fixture-key")
			}))
			defer host.Close()
			_, err := (&Chat{}).modelCompletion(context.Background(), ModelConfig{API: "responses", Endpoint: host.URL, Model: "fixture", APIKey: "private-fixture-key"}, nil, nil, nil)
			var failure *ModelError
			if !errors.As(err, &failure) || failure.HTTPStatus != status || failure.Retryable != (status == 429 || status >= 500) || strings.Contains(err.Error(), "private-fixture-key") {
				t.Fatal("HTTP error classification or redaction failed", err)
			}
		})
	}
	stream := false
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.Copy(w, strings.NewReader(strings.Repeat(" ", (8<<20)+1)))
	}))
	defer host.Close()
	response, err := (&Chat{}).modelCompletion(context.Background(), ModelConfig{API: "responses", Stream: &stream, Endpoint: host.URL, Model: "fixture"}, nil, nil, nil)
	if err == nil || len(response.Message.ToolCalls) != 0 || !strings.Contains(err.Error(), "response budget") {
		t.Fatal("HTTP response exceeded budget", err)
	}
}

func TestModelRequiresExplicitStreamingMediaType(t *testing.T) {
	for _, contentType := range []string{"application/json", "text/event-stream-invalid", "text/event-stream; invalid"} {
		host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			fmt.Fprint(w, responsesTextStream())
		}))
		_, err := (&Chat{}).modelCompletion(context.Background(), ModelConfig{API: "responses", Endpoint: host.URL, Model: "fixture"}, nil, nil, nil)
		host.Close()
		var failure *ModelError
		if !errors.As(err, &failure) || failure.Code != "media_type" {
			t.Fatal("invalid stream media type accepted", err)
		}
	}
}

package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"competition2026/product/platform/internal/ai"
	"competition2026/product/platform/internal/app"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func responsesApplication(t *testing.T) *app.Application {
	t.Helper()
	dir := t.TempDir()
	a, err := app.Open(context.Background(), app.Options{Mode: "cloud", NodeID: "responses-http", DSN: filepath.Join(dir, "db"), KeyFile: filepath.Join(dir, "key"), BootstrapPassword: "test-password-1234", Seed: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func responseFixtureEvent(kind string, data any) string {
	raw, _ := json.Marshal(data)
	return "event: " + kind + "\r\ndata: " + string(raw) + "\r\n\r\n"
}

func fixtureToolResponse(round int, name string, args any) (map[string]any, string) {
	argumentBytes, _ := json.Marshal(args)
	arguments := string(argumentBytes)
	item := map[string]any{"id": fmt.Sprintf("function-%d", round), "type": "function_call", "status": "completed", "call_id": fmt.Sprintf("call-%d", round), "name": name, "arguments": arguments}
	response := map[string]any{"id": fmt.Sprintf("response-%d", round), "status": "completed", "output": []any{map[string]any{"id": fmt.Sprintf("reasoning-%d", round), "type": "reasoning", "summary": []any{}, "encrypted_content": "opaque-http-fixture"}, item}}
	added := map[string]any{}
	for key, value := range item {
		added[key] = value
	}
	added["arguments"] = ""
	added["status"] = "in_progress"
	stream := responseFixtureEvent("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 1, "item": added})
	runes := []rune(arguments)
	for _, fragment := range []string{string(runes[:len(runes)/3]), string(runes[len(runes)/3 : len(runes)*2/3]), string(runes[len(runes)*2/3:])} {
		stream += responseFixtureEvent("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "item_id": item["id"], "output_index": 1, "delta": fragment})
	}
	stream += responseFixtureEvent("response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "item_id": item["id"], "output_index": 1, "arguments": arguments})
	stream += responseFixtureEvent("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 1, "item": item})
	return response, stream + responseFixtureEvent("response.completed", map[string]any{"type": "response.completed", "response": response})
}

func writeFixtureResponse(w http.ResponseWriter, stream bool, response any, events string) {
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	// Fragment HTTP bytes as well as SSE argument events, including UTF-8.
	for len(events) > 0 {
		size := 43
		if len(events) < size {
			size = len(events)
		}
		io.WriteString(w, events[:size])
		w.(http.Flusher).Flush()
		events = events[size:]
	}
}

func writeFixtureText(w http.ResponseWriter, stream bool, text string) {
	response := map[string]any{"id": "final-response", "status": "completed", "output": []any{map[string]any{"id": "final-message", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}}}
	events := responseFixtureEvent("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "final-message", "output_index": 0, "content_index": 0, "delta": text}) + responseFixtureEvent("response.completed", map[string]any{"type": "response.completed", "response": response})
	writeFixtureResponse(w, stream, response, events)
}

func fixtureOutputs(t *testing.T, r *http.Request) ([]string, []json.RawMessage) {
	t.Helper()
	if r.URL.Path != "/v1/responses" || r.Method != "POST" {
		t.Error("wrong Responses endpoint")
	}
	var input struct {
		Input   []json.RawMessage `json:"input"`
		Store   bool              `json:"store"`
		Include []string          `json:"include"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		t.Error(err)
	}
	if input.Store || len(input.Include) != 1 || input.Include[0] != "reasoning.encrypted_content" {
		t.Error("Responses state retention configuration changed")
	}
	ids := []string{}
	outputs := []json.RawMessage{}
	for _, raw := range input.Input {
		var item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		}
		if json.Unmarshal(raw, &item) != nil {
			t.Error("invalid input item")
			continue
		}
		if item.Type == "function_call_output" {
			ids = append(ids, item.CallID)
			outputs = append(outputs, json.RawMessage(item.Output))
		}
	}
	for index, id := range ids {
		if id != fmt.Sprintf("call-%d", index) {
			t.Error("tool result has a different call_id")
		}
	}
	if len(outputs) > 0 {
		combined, _ := json.Marshal(input.Input)
		if !strings.Contains(string(combined), "opaque-http-fixture") || !strings.Contains(string(combined), `"type":"reasoning"`) || !strings.Contains(string(combined), `"type":"function_call"`) {
			t.Error("Responses continuation lost native items")
		}
	}
	return ids, outputs
}

func postFixtureAssistant(t *testing.T, a *app.Application, login string) string {
	t.Helper()
	token, _, err := a.Server.Identity.Login(context.Background(), login, "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(a.Server.Handler())
	defer host.Close()
	body := []byte(`{"messages":[{"role":"user","content":"执行授权范围内的模拟查询或草稿流程"}]}`)
	request, _ := http.NewRequest("POST", host.URL+"/api/sf/v1/assistant", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal("assistant HTTP failed", err)
	}
	return string(raw)
}

func TestResponsesHTTPAssistantThreeDraftKinds(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, kind := range []string{"analysis", "alarm", "strategy"} {
			t.Run(fmt.Sprintf("%s/stream=%v", kind, stream), func(t *testing.T) {
				a := responsesApplication(t)
				before, _ := a.Store.List(context.Background(), "definition")
				requests := 0
				draftID := fmt.Sprintf("responses-%s-%v", kind, stream)
				mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					_, outputs := fixtureOutputs(t, r)
					name := ""
					var args any
					switch len(outputs) {
					case 0:
						name = "list_definitions"
						args = map[string]any{"kind": kind}
					case 1:
						var page struct {
							Items []model.Definition `json:"items"`
						}
						if err := store.DecodeJSON(outputs[0], &page); err != nil || len(page.Items) == 0 {
							t.Error("definition discovery failed")
							writeFixtureText(w, stream, "failed")
							return
						}
						name = "get_definition"
						args = map[string]any{"id": page.Items[0].ID}
					case 2:
						var definition model.Definition
						if err := store.DecodeJSON(outputs[1], &definition); err != nil || definition.Kind != kind {
							t.Error("definition read failed")
							writeFixtureText(w, stream, "failed")
							return
						}
						definition.ID = draftID
						definition.Name = "Responses模拟草稿"
						definition.Version = 0
						definition.Status = "draft"
						definition.EffectiveMS = 0
						if kind == "strategy" {
							definition.Nodes[1].Params["value"] = json.Number("9223372036854775807")
						}
						name = "save_draft"
						args = map[string]any{"draft": model.Draft{ID: draftID, Definition: definition}, "expected_version": 0}
					case 3:
						name = "validate_draft"
						args = map[string]any{"id": draftID}
					case 4:
						name = "diff_draft"
						args = map[string]any{"id": draftID}
					default:
						for _, output := range outputs {
							var failure struct {
								Error string `json:"error"`
							}
							json.Unmarshal(output, &failure)
							if failure.Error != "" {
								t.Error("business tool failed", failure.Error)
							}
						}
						writeFixtureText(w, stream, "三类流程之一已完成")
						return
					}
					response, events := fixtureToolResponse(len(outputs), name, args)
					writeFixtureResponse(w, stream, response, events)
				}))
				defer mock.Close()
				a.Server.Chat.(*ai.Chat).Default = ai.ModelConfig{Provider: "openai-compatible", API: "responses", Stream: &stream, Endpoint: mock.URL + "/v1", Model: "local-response-fixture", TimeoutMS: 5000}
				body := postFixtureAssistant(t, a, "admin")
				if requests != 6 || !strings.Contains(body, `"type":"done"`) || strings.Contains(body, `"type":"error"`) || !strings.Contains(body, "validate_draft") || !strings.Contains(body, "diff_draft") {
					t.Fatal("Responses HTTP draft flow did not complete", requests, body)
				}
				doc, err := a.Store.Get(context.Background(), "draft", draftID)
				if err != nil || doc.Version != 1 {
					t.Fatal("draft was not saved once", err)
				}
				if kind == "strategy" && !strings.Contains(string(doc.Data), `"value":9223372036854775807`) {
					t.Fatal("Responses draft integer rounded")
				}
				if _, err = a.Store.Get(context.Background(), "definition", draftID); err != store.ErrNotFound {
					t.Fatal("assistant published a draft", err)
				}
				after, _ := a.Store.List(context.Background(), "definition")
				if store.Hash(after) != store.Hash(before) {
					t.Fatal("Responses assistant changed published definitions")
				}
			})
		}
	}
}

func TestResponsesHTTPAssistantPreciseQueryToolOutput(t *testing.T) {
	a := responsesApplication(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	_, err := a.Store.Ingest(ctx, store.IngestBatch{MessageID: "responses-exact-observation", SourceID: "edge-fixture", Points: []model.Observation{{ID: "responses-exact-point", DeviceID: "counter-1", Key: "total", Value: json.Number("9223372036854775807"), ObservedMS: now, Quality: "GOOD", TimeSource: "device"}}})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, outputs := fixtureOutputs(t, r)
		if len(outputs) == 0 {
			response, events := fixtureToolResponse(0, "query_data", map[string]any{"device_ids": "counter-1", "keys": "total", "from_ms": now - 1000, "to_ms": now + 1000, "limit": 100})
			writeFixtureResponse(w, true, response, events)
			return
		}
		if !strings.Contains(string(outputs[0]), `"value":9223372036854775807`) {
			t.Error("query tool output rounded the original observation")
		}
		writeFixtureText(w, true, "整数查询完成")
	}))
	defer mock.Close()
	a.Server.Chat.(*ai.Chat).Default = ai.ModelConfig{Provider: "openai", API: "responses", Endpoint: mock.URL + "/v1", Model: "fixture", TimeoutMS: 5000}
	if body := postFixtureAssistant(t, a, "admin"); requests != 2 || !strings.Contains(body, "整数查询完成") || !strings.Contains(body, `"type":"done"`) {
		t.Fatal("query loop failed", body)
	}
}

func TestResponsesHTTPAssistantRejectsUnfinishedOrConflictingWriteCalls(t *testing.T) {
	for _, mode := range []string{"truncated", "failed", "incomplete", "argument conflict", "duplicate argument", "final item identity conflict"} {
		t.Run(mode, func(t *testing.T) {
			a := responsesApplication(t)
			definition := app.ExampleDefinitions()[0]
			definition.ID = "rejected-response"
			definition.Version = 0
			definition.SchemaVersion = "1.0"
			definition.GroupID = "factory"
			response, events := fixtureToolResponse(0, "save_draft", map[string]any{"draft": model.Draft{ID: definition.ID, Definition: definition}, "expected_version": 0})
			terminal := strings.Index(events, "event: response.completed")
			switch mode {
			case "truncated":
				events = events[:terminal]
			case "failed":
				events = events[:terminal] + responseFixtureEvent("response.failed", map[string]any{"type": "response.failed", "response": map[string]any{"status": "failed", "error": map[string]any{"message": "fixture upstream error"}}})
			case "incomplete":
				events = events[:terminal] + responseFixtureEvent("response.incomplete", map[string]any{"type": "response.incomplete", "response": map[string]any{"status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"}}})
			case "argument conflict":
				events = strings.Replace(events, `\"expected_version\":0`, `\"expected_version\":1`, 1)
			case "duplicate argument":
				output := response["output"].([]any)
				item := output[1].(map[string]any)
				item["arguments"] = strings.Replace(item["arguments"].(string), `"expected_version":0`, `"expected_version":0,"expected_version":1`, 1)
				events = responseFixtureEvent("response.completed", map[string]any{"type": "response.completed", "response": response})
			case "final item identity conflict":
				events = events[:terminal] + strings.Replace(events[terminal:], `"id":"function-0"`, `"id":"function-changed"`, 1)
			}
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeFixtureResponse(w, true, response, events) }))
			defer mock.Close()
			a.Server.Chat.(*ai.Chat).Default = ai.ModelConfig{Provider: "openai", API: "responses", Endpoint: mock.URL + "/v1", Model: "fixture", TimeoutMS: 5000}
			body := postFixtureAssistant(t, a, "admin")
			if !strings.Contains(body, `"type":"error"`) || strings.Contains(body, `"type":"tool"`) || strings.Contains(body, `"type":"done"`) {
				t.Fatal("invalid response entered the tool loop", body)
			}
			if _, err := a.Store.Get(context.Background(), "draft", definition.ID); err != store.ErrNotFound {
				t.Fatal("unfinished response saved a draft", err)
			}
		})
	}
}

func TestResponsesHTTPAssistantReportsToolPermissionsAndVersionConflict(t *testing.T) {
	for _, mode := range []string{"readonly draft", "resource permission", "version conflict"} {
		t.Run(mode, func(t *testing.T) {
			a := responsesApplication(t)
			ctx := context.Background()
			login := "viewer"
			name := "save_draft"
			definition := app.ExampleDefinitions()[0]
			definition.ID = "permission-draft"
			definition.Version = 0
			definition.SchemaVersion = "1.0"
			definition.GroupID = "factory"
			var args any = map[string]any{"draft": model.Draft{ID: definition.ID, Definition: definition}, "expected_version": 0}
			if mode == "resource permission" {
				userDoc, _ := a.Store.Get(ctx, "user", "viewer")
				user, _ := store.Decode[model.User](userDoc)
				user.Resources = []string{"counter-1"}
				if _, err := a.Server.Identity.CreateUser(ctx, model.Actor{UserID: "admin"}, user, "", "", userDoc.Version); err != nil {
					t.Fatal(err)
				}
				name = "query_data"
				args = map[string]any{"device_ids": "climate-1", "keys": "temperature", "from_ms": 0, "to_ms": 1}
			} else if mode == "version conflict" {
				login = "admin"
				if _, err := a.Server.Engine.SaveDraft(ctx, model.Actor{UserID: "admin"}, model.Draft{ID: definition.ID, Definition: definition}, 0); err != nil {
					t.Fatal(err)
				}
			}
			requests := 0
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				_, outputs := fixtureOutputs(t, r)
				if len(outputs) == 0 {
					response, events := fixtureToolResponse(0, name, args)
					writeFixtureResponse(w, true, response, events)
					return
				}
				var failure struct {
					Error string `json:"error"`
				}
				if json.Unmarshal(outputs[0], &failure) != nil || failure.Error == "" {
					t.Error("tool rejection was not supplied to the model")
				}
				writeFixtureText(w, true, "工具请求已拒绝")
			}))
			defer mock.Close()
			a.Server.Chat.(*ai.Chat).Default = ai.ModelConfig{Provider: "openai", API: "responses", Endpoint: mock.URL + "/v1", Model: "fixture", TimeoutMS: 5000}
			body := postFixtureAssistant(t, a, login)
			if requests != 2 || !strings.Contains(body, "工具请求已拒绝") || !strings.Contains(body, `"type":"done"`) {
				t.Fatal("tool rejection loop did not complete", body)
			}
			doc, err := a.Store.Get(ctx, "draft", definition.ID)
			if mode == "version conflict" {
				if err != nil || doc.Version != 1 {
					t.Fatal("version conflict changed draft", err)
				}
			} else if err != store.ErrNotFound {
				t.Fatal("unauthorized write saved a draft", err)
			}
		})
	}
}

func TestResponsesHTTPAssistantCancellationStopsBeforeToolExecution(t *testing.T) {
	a := responsesApplication(t)
	definition := app.ExampleDefinitions()[0]
	definition.ID = "cancelled-response"
	definition.Version = 0
	definition.SchemaVersion = "1.0"
	definition.GroupID = "factory"
	response, events := fixtureToolResponse(0, "save_draft", map[string]any{"draft": model.Draft{ID: definition.ID, Definition: definition}, "expected_version": 0})
	events = events[:strings.Index(events, "event: response.completed")]
	started := make(chan struct{})
	closed := make(chan struct{})
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFixtureResponse(w, true, response, events)
		close(started)
		<-r.Context().Done()
		close(closed)
	}))
	defer mock.Close()
	a.Server.Chat.(*ai.Chat).Default = ai.ModelConfig{Provider: "openai", API: "responses", Endpoint: mock.URL + "/v1", Model: "fixture", TimeoutMS: 5000}
	token, _, err := a.Server.Identity.Login(context.Background(), "admin", "test-password-1234", "", false, "test")
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(a.Server.Handler())
	defer host.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", host.URL+"/api/sf/v1/assistant", strings.NewReader(`{"messages":[{"role":"user","content":"保存草稿"}]}`))
	request.Header.Set("Authorization", "Bearer "+token)
	done := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if response != nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("mock model was not called")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("assistant request did not stop")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("model stream did not observe cancellation")
	}
	if _, err = a.Store.Get(context.Background(), "draft", definition.ID); err != store.ErrNotFound {
		t.Fatal("cancelled response saved a draft", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err = a.Store.DB.QueryRow("SELECT count(*) FROM documents WHERE kind='session' AND data LIKE '%\"delegated_ai\":true%'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cancelled assistant retained its delegated session")
}

func TestResponsesHTTPAssistantModelTimeoutConfiguration(t *testing.T) {
	for _, timeout := range []int64{300001} {
		a := responsesApplication(t)
		a.Server.Chat.(*ai.Chat).Default = ai.ModelConfig{API: "responses", Endpoint: "http://127.0.0.1:9", Model: "fixture", TimeoutMS: timeout}
		token, _, err := a.Server.Identity.Login(context.Background(), "admin", "test-password-1234", "", false, "test")
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "/api/sf/v1/assistant", strings.NewReader(`{"messages":[{"role":"user","content":"query"}]}`))
		request.Header.Set("Authorization", "Bearer "+token)
		writer := httptest.NewRecorder()
		a.Server.Handler().ServeHTTP(writer, request)
		if writer.Code != 503 || !strings.Contains(writer.Body.String(), "five minutes") {
			t.Fatal("invalid configured timeout accepted", strconv.Itoa(writer.Code))
		}
	}
}

package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
)

// ModelMessage is a complete message. StreamEvent is deliberately separate:
// partial argument fragments never become executable ToolCalls.
type ModelMessage struct {
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content,omitempty"`
	ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	// NativeItems preserve Responses reasoning and function items across rounds.
	NativeItems []json.RawMessage `json:"native_items,omitempty"`
}
type ContentBlock struct {
	Type string          `json:"type"`
	Text string          `json:"text,omitempty"`
	Raw  json.RawMessage `json:"raw,omitempty"`
}

func (m ModelMessage) Text() string {
	var text strings.Builder
	for _, block := range m.Content {
		if block.Type == "text" || block.Type == "refusal" {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}
func textMessage(role, text string) ModelMessage {
	return ModelMessage{Role: role, Content: []ContentBlock{{Type: "text", Text: text}}}
}

type ModelResponse struct {
	ID       string          `json:"id"`
	Provider string          `json:"provider"`
	API      string          `json:"api"`
	Status   string          `json:"status"`
	Message  ModelMessage    `json:"message"`
	Usage    json.RawMessage `json:"usage,omitempty"`
}
type StreamEvent struct {
	Type         string          `json:"type"`
	ItemID       string          `json:"item_id,omitempty"`
	OutputIndex  int             `json:"output_index,omitempty"`
	ContentIndex int             `json:"content_index,omitempty"`
	Text         string          `json:"text,omitempty"`
	CallID       string          `json:"call_id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Raw          json.RawMessage `json:"raw,omitempty"`
}
type ModelError struct {
	Provider   string `json:"provider"`
	API        string `json:"api"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Retryable  bool   `json:"retryable"`
	Cause      error  `json:"-"`
}

func (e *ModelError) Error() string { return fmt.Sprintf("%s/%s: %s", e.Provider, e.API, e.Message) }
func (e *ModelError) Unwrap() error { return e.Cause }

func responseError(cfg ModelConfig, code, message string, err error) *ModelError {
	if errors.Is(err, context.Canceled) {
		code, message = "cancelled", "model request cancelled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code, message = "timeout", "model request timed out"
	}
	return &ModelError{Provider: cfg.Provider, API: cfg.API, Code: code, Message: redactModelError(message, cfg.APIKey), Cause: err}
}

type providerAdapter interface {
	path() string
	request(ModelConfig, []ModelMessage, []Tool, bool) (any, error)
	complete([]byte) (ModelResponse, error)
	stream(io.Reader, func(StreamEvent) error) (ModelResponse, error)
}

func selectedAdapter(cfg ModelConfig) (providerAdapter, ModelConfig, error) {
	if cfg.Provider == "" {
		cfg.Provider = "openai-compatible"
	}
	if cfg.API == "" {
		cfg.API = "chat_completions"
	}
	if cfg.Provider != "openai" && cfg.Provider != "openai-compatible" {
		return nil, cfg, errors.New("unsupported model provider: " + cfg.Provider)
	}
	switch cfg.API {
	case "chat_completions":
		return chatCompletionsAdapter{}, cfg, nil
	case "responses":
		return responsesAdapter{}, cfg, nil
	default:
		return nil, cfg, errors.New("unsupported model API: " + cfg.API)
	}
}

func (c *Chat) modelCompletion(ctx context.Context, cfg ModelConfig, messages []ModelMessage, tools []Tool, onEvent func(StreamEvent) error) (ModelResponse, error) {
	adapter, cfg, err := selectedAdapter(cfg)
	if err != nil {
		return ModelResponse{}, err
	}
	stream := cfg.Stream == nil || *cfg.Stream
	body, err := adapter.request(cfg, messages, tools, stream)
	if err != nil {
		return ModelResponse{}, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return ModelResponse{}, err
	}
	if len(encoded) > 4<<20 {
		return ModelResponse{}, errors.New("model request exceeds context budget")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.Endpoint, "/")+adapter.path(), bytes.NewReader(encoded))
	if err != nil {
		return ModelResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("model endpoint redirects are disabled") }}
	}
	response, err := client.Do(request)
	if err != nil {
		failure := responseError(cfg, "transport", "model request failed", err)
		failure.Retryable = ctx.Err() == nil
		return ModelResponse{}, failure
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ModelResponse{}, &ModelError{Provider: cfg.Provider, API: cfg.API, Code: "http", Message: fmt.Sprintf("model endpoint returned HTTP %d", response.StatusCode), HTTPStatus: response.StatusCode, Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	if onEvent == nil {
		onEvent = func(StreamEvent) error { return nil }
	}
	consumer := onEvent
	onEvent = func(event StreamEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := consumer(event); err != nil {
			return err
		}
		return ctx.Err()
	}
	var result ModelResponse
	if stream {
		mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if mediaErr != nil || mediaType != "text/event-stream" {
			return ModelResponse{}, &ModelError{Provider: cfg.Provider, API: cfg.API, Code: "media_type", Message: "model did not return an event stream"}
		}
		result, err = adapter.stream(response.Body, onEvent)
	} else {
		var raw []byte
		raw, err = readBounded(response.Body, 8<<20)
		if err == nil {
			result, err = adapter.complete(raw)
		}
		if err == nil && result.Message.Text() != "" {
			err = onEvent(StreamEvent{Type: "text_delta", Text: result.Message.Text()})
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return ModelResponse{}, responseError(cfg, "response", err.Error(), err)
	}
	result.Provider, result.API = cfg.Provider, cfg.API
	if err = onEvent(StreamEvent{Type: "completed"}); err != nil {
		return ModelResponse{}, responseError(cfg, "response", err.Error(), err)
	}
	return result, nil
}
func redactModelError(message, secret string) string {
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}
func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err == nil && int64(len(value)) > maximum {
		err = errors.New("model response budget exceeded")
	}
	return value, err
}

// readSSE supports CRLF, multiline data and a final event without a blank line.
// It stops only when its consumer confirms a valid API-specific terminal event.
func readSSE(reader io.Reader, consume func(string, []byte) (bool, error)) error {
	limited := &io.LimitedReader{R: reader, N: (8 << 20) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var event string
	var data []string
	flush := func() (bool, error) {
		if len(data) == 0 {
			event = ""
			return false, nil
		}
		stop, err := consume(event, []byte(strings.Join(data, "\n")))
		event, data = "", nil
		return stop, err
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if stop, err := flush(); err != nil || stop {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if limited.N <= 0 {
		return errors.New("model stream budget exceeded")
	}
	if stop, err := flush(); err != nil || stop {
		return err
	}
	return errors.New("model stream ended without a completion marker")
}
func validCalls(calls []ToolCall) error {
	if len(calls) > 16 {
		return errors.New("tool call budget exceeded")
	}
	ids := map[string]bool{}
	for _, call := range calls {
		if call.ID == "" || len(call.ID) > 200 || call.Function.Name == "" || len(call.Function.Name) > 200 || call.Type != "function" || ids[call.ID] || len(call.Function.Arguments) > 256<<10 {
			return errors.New("incomplete or duplicate tool call")
		}
		ids[call.ID] = true
		if err := validateToolArguments(call.Function.Arguments); err != nil {
			return errors.New("tool arguments must be a complete JSON object")
		}
	}
	return nil
}

// Token validation rejects duplicate names before decoding into a map could
// silently select one value. Number tokens retain their exact JSON spelling.
func validateToolArguments(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("tool arguments must be an object")
	}
	var object func(int) error
	var value func(int) error
	object = func(depth int) error {
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			name, ok := token.(string)
			if err != nil || !ok || seen[name] {
				return errors.New("duplicate or invalid tool argument")
			}
			seen[name] = true
			if err = value(depth + 1); err != nil {
				return err
			}
		}
		token, err := decoder.Token()
		if err != nil || token != json.Delim('}') {
			return errors.New("incomplete tool argument object")
		}
		return nil
	}
	value = func(depth int) error {
		if depth > 32 {
			return errors.New("tool argument depth exceeds 32")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'):
			return object(depth)
		case json.Delim('['):
			for decoder.More() {
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim(']') {
				return errors.New("incomplete tool argument array")
			}
		case json.Delim('}'), json.Delim(']'):
			return errors.New("unexpected tool argument delimiter")
		}
		return nil
	}
	if err = object(0); err != nil {
		return err
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing tool argument content")
	}
	return nil
}

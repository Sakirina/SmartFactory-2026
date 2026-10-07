package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

type chatCompletionsAdapter struct{}

func (chatCompletionsAdapter) path() string { return "/chat/completions" }
func (chatCompletionsAdapter) request(cfg ModelConfig, messages []ModelMessage, tools []Tool, stream bool) (any, error) {
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		content, err := requestContent(message, "chat_completions")
		if err != nil {
			return nil, err
		}
		entry := map[string]any{"role": message.Role, "content": content}
		if len(message.ToolCalls) > 0 {
			entry["tool_calls"] = message.ToolCalls
		}
		if message.ToolCallID != "" {
			entry["tool_call_id"] = message.ToolCallID
		}
		input = append(input, entry)
	}
	functions := make([]any, 0, len(tools))
	for _, tool := range tools {
		functions = append(functions, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema}})
	}
	return map[string]any{"model": cfg.Model, "messages": input, "tools": functions, "stream": stream, "store": false}, nil
}

func requestContent(message ModelMessage, api string) (any, error) {
	if len(message.Content) == 0 {
		return nil, nil
	}
	if len(message.Content) == 1 && message.Content[0].Type == "text" && len(message.Content[0].Raw) == 0 {
		return message.Content[0].Text, nil
	}
	parts := make([]any, 0, len(message.Content))
	for _, block := range message.Content {
		var part map[string]json.RawMessage
		if len(block.Raw) != 0 {
			if err := json.Unmarshal(block.Raw, &part); err != nil || part == nil {
				return nil, errors.New("invalid preserved content block")
			}
		} else {
			part = map[string]json.RawMessage{}
		}
		kind := block.Type
		if kind == "text" {
			if api == "responses" {
				kind = "input_text"
				if message.Role == "assistant" {
					kind = "output_text"
				}
			}
			part["text"], _ = json.Marshal(block.Text)
		} else if kind == "refusal" {
			part["refusal"], _ = json.Marshal(block.Text)
		} else if len(block.Raw) == 0 {
			return nil, errors.New("content block has no preserved payload")
		}
		if kind == "" {
			return nil, errors.New("content block has no type")
		}
		part["type"], _ = json.Marshal(kind)
		parts = append(parts, part)
	}
	return parts, nil
}

type chatOutput struct {
	ID    string          `json:"id"`
	Usage json.RawMessage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Refusal   string          `json:"refusal"`
			ToolCalls []ToolCall      `json:"tool_calls"`
		} `json:"message"`
		Delta struct {
			Content   json.RawMessage `json:"content"`
			Refusal   string          `json:"refusal"`
			ToolCalls []ToolCall      `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func parseBlocks(raw json.RawMessage) ([]ContentBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil, nil
		}
		return []ContentBlock{{Type: "text", Text: text}}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, errors.New("invalid content block sequence")
	}
	if len(parts) > 128 {
		return nil, errors.New("content block budget exceeded")
	}
	blocks := make([]ContentBlock, 0, len(parts))
	for _, part := range parts {
		var content struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		}
		if err := json.Unmarshal(part, &content); err != nil {
			return nil, err
		}
		kind := content.Type
		if kind == "" {
			return nil, errors.New("content block has no type")
		}
		if kind == "output_text" || kind == "input_text" {
			kind = "text"
		}
		if kind == "refusal" {
			content.Text = content.Refusal
		}
		blocks = append(blocks, ContentBlock{Type: kind, Text: content.Text, Raw: append(json.RawMessage(nil), part...)})
	}
	return blocks, nil
}
func validChatFinish(reason *string) error {
	if reason == nil || (*reason != "stop" && *reason != "tool_calls") {
		return errors.New("model response ended before completion")
	}
	return nil
}
func validChatToolFinish(reason string, count int) error {
	if (reason == "tool_calls") != (count > 0) {
		return errors.New("model finish reason does not match tool calls")
	}
	return nil
}
func (chatCompletionsAdapter) complete(raw []byte) (ModelResponse, error) {
	var response chatOutput
	if err := json.Unmarshal(raw, &response); err != nil {
		return ModelResponse{}, err
	}
	if response.Error != nil {
		return ModelResponse{}, errors.New(response.Error.Message)
	}
	for _, choice := range response.Choices {
		if choice.Index != 0 {
			continue
		}
		if err := validChatFinish(choice.FinishReason); err != nil {
			return ModelResponse{}, err
		}
		blocks, err := parseBlocks(choice.Message.Content)
		if err != nil {
			return ModelResponse{}, err
		}
		if choice.Message.Refusal != "" {
			blocks = append(blocks, ContentBlock{Type: "refusal", Text: choice.Message.Refusal})
		}
		if len((ModelMessage{Content: blocks}).Text()) > 1<<20 {
			return ModelResponse{}, errors.New("model text budget exceeded")
		}
		if err = validCalls(choice.Message.ToolCalls); err != nil {
			return ModelResponse{}, err
		}
		if err = validChatToolFinish(*choice.FinishReason, len(choice.Message.ToolCalls)); err != nil {
			return ModelResponse{}, err
		}
		return ModelResponse{ID: response.ID, Status: "completed", Usage: response.Usage, Message: ModelMessage{Role: "assistant", Content: blocks, ToolCalls: choice.Message.ToolCalls}}, nil
	}
	return ModelResponse{}, errors.New("model response has no first choice")
}
func (chatCompletionsAdapter) stream(reader io.Reader, emit func(StreamEvent) error) (ModelResponse, error) {
	result := ModelResponse{Status: "completed", Message: ModelMessage{Role: "assistant"}}
	var text, refusal strings.Builder
	calls := map[int]*ToolCall{}
	finished := false
	finishReason := ""
	err := readSSE(reader, func(_ string, raw []byte) (bool, error) {
		if string(raw) == "[DONE]" {
			if !finished {
				return true, errors.New("model stream has no successful finish reason")
			}
			return true, nil
		}
		var chunk chatOutput
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return false, fmt.Errorf("invalid model stream JSON: %w", err)
		}
		if chunk.Error != nil {
			return false, errors.New(chunk.Error.Message)
		}
		if chunk.ID != "" {
			if result.ID != "" && result.ID != chunk.ID {
				return false, errors.New("model response id changed")
			}
			result.ID = chunk.ID
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			result.Usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			blocks, err := parseBlocks(choice.Delta.Content)
			if err != nil {
				return false, err
			}
			if finished && (len(blocks) > 0 || choice.Delta.Refusal != "" || len(choice.Delta.ToolCalls) > 0) {
				return false, errors.New("model emitted content after finish")
			}
			for _, block := range blocks {
				if block.Type != "text" {
					return false, errors.New("unsupported streamed content block")
				}
				text.WriteString(block.Text)
				if text.Len() > 1<<20 {
					return false, errors.New("model text budget exceeded")
				}
				if err := emit(StreamEvent{Type: "text_delta", Text: block.Text}); err != nil {
					return false, err
				}
			}
			if choice.Delta.Refusal != "" {
				refusal.WriteString(choice.Delta.Refusal)
				if refusal.Len() > 1<<20 {
					return false, errors.New("model text budget exceeded")
				}
				if err := emit(StreamEvent{Type: "text_delta", Text: choice.Delta.Refusal}); err != nil {
					return false, err
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				if delta.Index < 0 || delta.Index >= 16 {
					return false, errors.New("tool call budget exceeded")
				}
				call := calls[delta.Index]
				if call == nil {
					call = &ToolCall{Index: delta.Index, Type: "function"}
					calls[delta.Index] = call
				}
				if delta.ID != "" {
					if call.ID != "" && call.ID != delta.ID {
						return false, errors.New("tool call id changed")
					}
					call.ID = delta.ID
				}
				if delta.Type != "" && delta.Type != "function" {
					return false, errors.New("unsupported tool call type")
				}
				call.Function.Name += delta.Function.Name
				call.Function.Arguments += delta.Function.Arguments
				if len(call.Function.Arguments) > 256<<10 {
					return false, errors.New("tool argument budget exceeded")
				}
				if err := emit(StreamEvent{Type: "tool_arguments_delta", OutputIndex: delta.Index, CallID: call.ID, Name: call.Function.Name, Text: delta.Function.Arguments}); err != nil {
					return false, err
				}
			}
			if choice.FinishReason != nil {
				if err := validChatFinish(choice.FinishReason); err != nil {
					return false, err
				}
				finished = true
				finishReason = *choice.FinishReason
			}
		}
		return false, nil
	})
	if err != nil {
		return ModelResponse{}, err
	}
	if text.Len() > 0 {
		result.Message.Content = append(result.Message.Content, ContentBlock{Type: "text", Text: text.String()})
	}
	if refusal.Len() > 0 {
		result.Message.Content = append(result.Message.Content, ContentBlock{Type: "refusal", Text: refusal.String()})
	}
	indices := make([]int, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := *calls[index]
		call.Index = 0
		result.Message.ToolCalls = append(result.Message.ToolCalls, call)
	}
	if err := validCalls(result.Message.ToolCalls); err != nil {
		return ModelResponse{}, err
	}
	if err := validChatToolFinish(finishReason, len(result.Message.ToolCalls)); err != nil {
		return ModelResponse{}, err
	}
	return result, nil
}

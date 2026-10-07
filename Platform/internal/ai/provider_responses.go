package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type responsesAdapter struct{}

func (responsesAdapter) path() string { return "/responses" }
func (responsesAdapter) request(cfg ModelConfig, messages []ModelMessage, tools []Tool, stream bool) (any, error) {
	input := []any{}
	for _, message := range messages {
		if len(message.NativeItems) > 0 {
			for _, item := range message.NativeItems {
				input = append(input, item)
			}
			continue
		}
		if message.Role == "tool" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Text()})
			continue
		}
		if len(message.Content) != 0 {
			content, err := requestContent(message, "responses")
			if err != nil {
				return nil, err
			}
			input = append(input, map[string]any{"role": message.Role, "content": content})
		}
		for _, call := range message.ToolCalls {
			input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}
	functions := make([]any, 0, len(tools))
	for _, tool := range tools {
		functions = append(functions, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema, "strict": false})
	}
	return map[string]any{"model": cfg.Model, "input": input, "tools": functions, "stream": stream, "store": false, "include": []string{"reasoning.encrypted_content"}}, nil
}

type responseEnvelope struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Usage  json.RawMessage   `json:"usage"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}
type responseItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	Role      string          `json:"role"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Content   json.RawMessage `json:"content"`
	Summary   json.RawMessage `json:"summary"`
}

func (responsesAdapter) complete(raw []byte) (ModelResponse, error) {
	var response responseEnvelope
	if err := json.Unmarshal(raw, &response); err != nil {
		return ModelResponse{}, err
	}
	if response.Error != nil {
		return ModelResponse{}, errors.New(response.Error.Message)
	}
	if response.Status != "completed" {
		reason := response.Status
		if response.IncompleteDetails != nil {
			reason += "/" + response.IncompleteDetails.Reason
		}
		return ModelResponse{}, errors.New("model response ended before completion: " + reason)
	}
	if len(response.Output) > 128 {
		return ModelResponse{}, errors.New("model output item budget exceeded")
	}
	result := ModelResponse{ID: response.ID, Status: response.Status, Usage: response.Usage, Message: ModelMessage{Role: "assistant", NativeItems: response.Output}}
	for _, rawItem := range response.Output {
		var item responseItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return ModelResponse{}, err
		}
		if item.Type == "" {
			return ModelResponse{}, errors.New("response output item has no type")
		}
		if item.Status != "" && item.Status != "completed" {
			return ModelResponse{}, errors.New("incomplete response output item")
		}
		switch item.Type {
		case "message":
			blocks, err := parseBlocks(item.Content)
			if err != nil {
				return ModelResponse{}, err
			}
			result.Message.Content = append(result.Message.Content, blocks...)
		case "function_call":
			call := ToolCall{ID: item.CallID, Type: "function"}
			call.Function.Name = item.Name
			call.Function.Arguments = item.Arguments
			result.Message.ToolCalls = append(result.Message.ToolCalls, call)
		case "reasoning":
			var summaries []struct {
				Text string `json:"text"`
			}
			if len(item.Summary) > 0 && json.Unmarshal(item.Summary, &summaries) != nil {
				return ModelResponse{}, errors.New("invalid reasoning summary")
			}
			for _, summary := range summaries {
				result.Message.Content = append(result.Message.Content, ContentBlock{Type: "reasoning_summary", Text: summary.Text})
			}
		default:
			// Keep non-executable output items for the next model round.
			result.Message.Content = append(result.Message.Content, ContentBlock{Type: item.Type, Raw: rawItem})
		}
	}
	if len(result.Message.Text()) > 1<<20 {
		return ModelResponse{}, errors.New("model text budget exceeded")
	}
	if err := validCalls(result.Message.ToolCalls); err != nil {
		return ModelResponse{}, err
	}
	return result, nil
}
func (adapter responsesAdapter) stream(reader io.Reader, emit func(StreamEvent) error) (ModelResponse, error) {
	var result ModelResponse
	text := map[[2]int]string{}
	textItems := map[[2]int]string{}
	textBytes := 0
	responseID := ""
	calls := map[int]*responseItem{}
	finalArguments := map[int]bool{}
	completed := false
	err := readSSE(reader, func(event string, raw []byte) (bool, error) {
		var chunk struct {
			Type         string          `json:"type"`
			ResponseID   string          `json:"response_id"`
			Delta        string          `json:"delta"`
			ItemID       string          `json:"item_id"`
			OutputIndex  int             `json:"output_index"`
			ContentIndex int             `json:"content_index"`
			Item         json.RawMessage `json:"item"`
			Arguments    string          `json:"arguments"`
			Response     json.RawMessage `json:"response"`
			Message      string          `json:"message"`
		}
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return false, fmt.Errorf("invalid Responses event: %w", err)
		}
		if chunk.Type == "" {
			chunk.Type = event
		}
		if event != "" && chunk.Type != event {
			return false, errors.New("Responses event type mismatch")
		}
		if chunk.ResponseID != "" {
			if responseID != "" && responseID != chunk.ResponseID {
				return false, errors.New("Responses stream changed response identity")
			}
			responseID = chunk.ResponseID
		}
		if chunk.OutputIndex < 0 || chunk.OutputIndex >= 128 || chunk.ContentIndex < 0 || chunk.ContentIndex >= 128 {
			return false, errors.New("model output item budget exceeded")
		}
		switch chunk.Type {
		case "response.created", "response.in_progress":
			var response responseEnvelope
			if err := json.Unmarshal(chunk.Response, &response); err != nil {
				return false, err
			}
			if response.ID != "" {
				if responseID != "" && responseID != response.ID {
					return false, errors.New("Responses stream changed response identity")
				}
				responseID = response.ID
			}
		case "response.output_text.delta", "response.refusal.delta":
			key := [2]int{chunk.OutputIndex, chunk.ContentIndex}
			if previous, ok := textItems[key]; ok && previous != chunk.ItemID {
				return false, errors.New("content fragment changed item identity")
			}
			textItems[key] = chunk.ItemID
			text[key] += chunk.Delta
			textBytes += len(chunk.Delta)
			if textBytes > 1<<20 {
				return false, errors.New("model text budget exceeded")
			}
			return false, emit(StreamEvent{Type: "text_delta", ItemID: chunk.ItemID, OutputIndex: chunk.OutputIndex, ContentIndex: chunk.ContentIndex, Text: chunk.Delta})
		case "response.reasoning_summary_text.delta":
			return false, emit(StreamEvent{Type: "reasoning_summary_delta", ItemID: chunk.ItemID, OutputIndex: chunk.OutputIndex, Text: chunk.Delta})
		case "response.output_item.added":
			var item responseItem
			if err := json.Unmarshal(chunk.Item, &item); err != nil {
				return false, err
			}
			if item.Type == "function_call" {
				if calls[chunk.OutputIndex] != nil || len(calls) >= 16 {
					return false, errors.New("duplicate or excessive function item")
				}
				calls[chunk.OutputIndex] = &item
				return false, emit(StreamEvent{Type: "tool_call_start", ItemID: item.ID, OutputIndex: chunk.OutputIndex, CallID: item.CallID, Name: item.Name})
			}
		case "response.function_call_arguments.delta":
			call := calls[chunk.OutputIndex]
			if call == nil || call.ID != chunk.ItemID || finalArguments[chunk.OutputIndex] {
				return false, errors.New("function argument fragment has no matching open item")
			}
			call.Arguments += chunk.Delta
			if len(call.Arguments) > 256<<10 {
				return false, errors.New("tool argument budget exceeded")
			}
			return false, emit(StreamEvent{Type: "tool_arguments_delta", ItemID: chunk.ItemID, OutputIndex: chunk.OutputIndex, CallID: call.CallID, Text: chunk.Delta})
		case "response.function_call_arguments.done":
			call := calls[chunk.OutputIndex]
			if call == nil || call.ID != chunk.ItemID || (call.Arguments != "" && call.Arguments != chunk.Arguments) {
				return false, errors.New("final function arguments differ from streamed fragments")
			}
			if len(chunk.Arguments) > 256<<10 {
				return false, errors.New("tool argument budget exceeded")
			}
			call.Arguments = chunk.Arguments
			finalArguments[chunk.OutputIndex] = true
		case "response.output_item.done":
			var item responseItem
			if err := json.Unmarshal(chunk.Item, &item); err != nil {
				return false, err
			}
			if item.Type == "function_call" {
				call := calls[chunk.OutputIndex]
				if call == nil || call.ID != item.ID || call.CallID != item.CallID || call.Name != item.Name || (call.Arguments != "" && call.Arguments != item.Arguments) {
					return false, errors.New("completed function item changed identity or arguments")
				}
				*call = item
				finalArguments[chunk.OutputIndex] = true
			}
			return false, emit(StreamEvent{Type: "item_completed", ItemID: item.ID, OutputIndex: chunk.OutputIndex, Raw: chunk.Item})
		case "response.completed":
			var err error
			result, err = adapter.complete(chunk.Response)
			if err != nil {
				return true, err
			}
			if responseID != "" && result.ID != responseID {
				return true, errors.New("completed response changed response identity")
			}
			finalText := map[[2]int]bool{}
			for outputIndex, rawItem := range result.Message.NativeItems {
				var item responseItem
				if err = json.Unmarshal(rawItem, &item); err != nil {
					return true, err
				}
				if item.Type != "message" {
					continue
				}
				blocks, err := parseBlocks(item.Content)
				if err != nil {
					return true, err
				}
				for contentIndex, block := range blocks {
					if block.Type != "text" && block.Type != "refusal" {
						continue
					}
					key := [2]int{outputIndex, contentIndex}
					finalText[key] = true
					if fragments, ok := text[key]; ok {
						if fragments != block.Text || textItems[key] != item.ID {
							return true, errors.New("completed text differs from streamed content block")
						}
					} else if block.Text != "" {
						if err := emit(StreamEvent{Type: "text_delta", ItemID: item.ID, OutputIndex: outputIndex, ContentIndex: contentIndex, Text: block.Text}); err != nil {
							return true, err
						}
					}
				}
			}
			for key := range text {
				if !finalText[key] {
					return true, errors.New("streamed content block is absent from completed response")
				}
			}
			if len(calls) != len(result.Message.ToolCalls) {
				return true, errors.New("completed function count differs from streamed items")
			}
			for outputIndex, call := range calls {
				if outputIndex >= len(result.Message.NativeItems) {
					return true, errors.New("completed function moved output position")
				}
				var finalItem responseItem
				if err = json.Unmarshal(result.Message.NativeItems[outputIndex], &finalItem); err != nil || finalItem.Type != "function_call" || finalItem.ID != call.ID {
					return true, errors.New("completed function changed item identity")
				}
				found := false
				for _, final := range result.Message.ToolCalls {
					if final.ID == call.CallID && final.Function.Name == call.Name && final.Function.Arguments == call.Arguments {
						found = true
						break
					}
				}
				if !found {
					return true, errors.New("completed function differs from streamed item")
				}
			}
			completed = true
			return true, nil
		case "response.failed", "response.incomplete":
			_, err := adapter.complete(chunk.Response)
			if err == nil {
				err = errors.New("model response failed")
			}
			return true, err
		case "error":
			if chunk.Message == "" {
				chunk.Message = "model stream error"
			}
			return true, errors.New(chunk.Message)
		}
		return false, nil
	})
	if err != nil {
		return ModelResponse{}, err
	}
	if !completed {
		return ModelResponse{}, errors.New("Responses stream has no completed response")
	}
	return result, nil
}

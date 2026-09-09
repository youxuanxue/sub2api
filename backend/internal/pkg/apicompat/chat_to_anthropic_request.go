package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ChatCompletionsToAnthropicRequest retains Messages cache placement locally;
// these extensions must not be sent through the Responses wire format.
func ChatCompletionsToAnthropicRequest(req *ChatCompletionsRequest) (*AnthropicRequest, error) {
	responses, err := ChatCompletionsToResponses(req)
	if err != nil {
		return nil, err
	}
	out, err := ResponsesToAnthropicRequest(responses)
	if err != nil {
		return nil, err
	}
	for i, tool := range req.Tools {
		if tool.CacheControl != nil && i < len(out.Tools) {
			out.Tools[i].CacheControl = tool.CacheControl
		}
	}
	if req.ParallelToolCalls != nil && !*req.ParallelToolCalls && len(out.Tools) > 0 {
		choice := map[string]any{"type": "auto"}
		if len(out.ToolChoice) > 0 {
			if err := json.Unmarshal(out.ToolChoice, &choice); err != nil {
				return nil, err
			}
		}
		if choice["type"] != "none" {
			choice["disable_parallel_tool_use"] = true
		}
		out.ToolChoice, err = json.Marshal(choice)
		if err != nil {
			return nil, err
		}
	}
	if hasChatMessageCache(req.Messages) {
		if err := ValidateChatToAnthropic(req); err != nil {
			return nil, err
		}
		out.System, out.Messages, err = chatCacheAwareAnthropicMessages(req)
		if err != nil {
			return nil, err
		}
	}
	if len(req.Stop) > 0 && string(req.Stop) != "null" {
		var stop string
		if json.Unmarshal(req.Stop, &stop) == nil {
			out.StopSeqs = []string{stop}
		} else if err := json.Unmarshal(req.Stop, &out.StopSeqs); err != nil {
			return nil, fmt.Errorf("convert stop sequences: %w", err)
		}
	}
	// The Responses intermediary imposes its own minimum; Messages does not.
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		out.MaxTokens = *req.MaxTokens
	}
	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		out.MaxTokens = *req.MaxCompletionTokens
	}
	return out, nil
}

func hasChatMessageCache(messages []ChatMessage) bool {
	for _, m := range messages {
		if m.CacheControl != nil {
			return true
		}
		var parts []ChatContentPart
		if json.Unmarshal(m.Content, &parts) == nil {
			for _, part := range parts {
				if part.CacheControl != nil {
					return true
				}
			}
		}
	}
	return false
}

func chatAnthropicTextBlocks(raw json.RawMessage) ([]AnthropicContentBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil, nil
		}
		return []AnthropicContentBlock{{Type: "text", Text: text}}, nil
	}
	var parts []ChatContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, err
	}
	blocks := make([]AnthropicContentBlock, 0, len(parts))
	for _, part := range parts {
		if part.Type != "text" || strings.TrimSpace(part.Text) == "" {
			return nil, fmt.Errorf("messages cache conversion requires nonempty text blocks")
		}
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: part.Text, CacheControl: part.CacheControl})
	}
	return blocks, nil
}

func chatCacheAwareAnthropicMessages(req *ChatCompletionsRequest) (json.RawMessage, []AnthropicMessage, error) {
	var system []AnthropicContentBlock
	if req.Instructions != "" {
		system = append(system, AnthropicContentBlock{Type: "text", Text: req.Instructions})
	}
	var messages []AnthropicMessage
	for _, m := range req.Messages {
		blocks, err := chatAnthropicTextBlocks(m.Content)
		if err != nil {
			return nil, nil, err
		}
		role := m.Role
		if role == "tool" {
			content, err := json.Marshal(blocks)
			if err != nil {
				return nil, nil, err
			}
			if len(blocks) == 0 {
				content = json.RawMessage(`"(empty)"`)
			}
			blocks = []AnthropicContentBlock{{Type: "tool_result", ToolUseID: ResponsesCallIDToAnthropic(m.ToolCallID), Content: content}}
			role = "user"
		}
		for _, call := range m.ToolCalls {
			blocks = append(blocks, AnthropicContentBlock{Type: "tool_use", ID: ResponsesCallIDToAnthropic(call.ID), Name: call.Function.Name, Input: json.RawMessage(call.Function.Arguments)})
		}
		if m.CacheControl != nil && len(blocks) > 0 {
			blocks[len(blocks)-1].CacheControl = m.CacheControl
		}
		if role == "system" || role == "developer" {
			system = append(system, blocks...)
			continue
		}
		if len(blocks) == 0 {
			continue
		}
		content, err := json.Marshal(blocks)
		if err != nil {
			return nil, nil, err
		}
		messages = append(messages, AnthropicMessage{Role: role, Content: content})
	}
	var systemJSON json.RawMessage
	if len(system) > 0 {
		systemJSON, _ = json.Marshal(system)
	}
	messages = mergeConsecutiveMessages(messages)
	messages = normalizeAnthropicToolPairing(messages)
	return systemJSON, mergeConsecutiveMessages(messages), nil
}

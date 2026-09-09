package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateChatToAnthropic checks the text/function-tool subset whose semantics
// the Messages converter preserves. Routing and cached conversion share it.
func ValidateChatToAnthropic(req *ChatCompletionsRequest) error {
	invalid := fmt.Errorf("unsupported Chat to Messages semantics")
	if len(req.Functions) > 0 || hasChatJSON(req.FunctionCall) || hasChatJSON(req.ResponseFormat) {
		return invalid
	}
	cacheCount := 0
	cache := func(value *AnthropicCacheControl) bool {
		if value == nil {
			return true
		}
		cacheCount++
		return value.Type == "ephemeral" && (value.TTL == "" || value.TTL == "5m" || value.TTL == "1h")
	}
	names := make(map[string]bool)
	for _, tool := range req.Tools {
		f := tool.Function
		if tool.Type != "function" || f == nil || strings.TrimSpace(f.Name) == "" || names[f.Name] || (f.Strict != nil && *f.Strict) || !cache(tool.CacheControl) {
			return invalid
		}
		var schema map[string]any
		if hasChatJSON(f.Parameters) && (json.Unmarshal(f.Parameters, &schema) != nil || schema == nil || (schema["type"] != nil && schema["type"] != "object")) {
			return invalid
		}
		names[f.Name] = true
	}
	if hasChatJSON(req.ToolChoice) {
		var choice string
		if json.Unmarshal(req.ToolChoice, &choice) == nil {
			if choice != "auto" && choice != "none" && choice != "required" {
				return invalid
			}
			if choice != "none" && len(names) == 0 {
				return invalid
			}
		} else {
			var choice struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if json.Unmarshal(req.ToolChoice, &choice) != nil || choice.Type != "function" || !names[choice.Function.Name] {
				return invalid
			}
		}
	}
	pending, seen := map[string]bool{}, map[string]bool{}
	for _, message := range req.Messages {
		if (len(pending) > 0 && message.Role != "tool") || message.FunctionCall != nil || message.ReasoningContent != "" || message.Reasoning != "" || !cache(message.CacheControl) {
			return invalid
		}
		switch message.Role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return invalid
		}
		blocks, err := chatAnthropicTextBlocks(message.Content)
		if err != nil {
			return invalid
		}
		for _, block := range blocks {
			if !cache(block.CacheControl) {
				return invalid
			}
		}
		// Message-level caching targets the final block; reject competing TTLs.
		if message.CacheControl != nil && message.Role != "tool" && len(message.ToolCalls) == 0 && len(blocks) > 0 && blocks[len(blocks)-1].CacheControl != nil {
			return invalid
		}
		if len(message.ToolCalls) > 0 && message.Role != "assistant" {
			return invalid
		}
		for _, call := range message.ToolCalls {
			var args map[string]any
			id := ResponsesCallIDToAnthropic(call.ID)
			if call.ID == "" || seen[id] || call.Type != "function" || !names[call.Function.Name] || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
				return invalid
			}
			seen[id], pending[call.ID] = true, true
		}
		if message.Role == "tool" {
			if !pending[message.ToolCallID] {
				return invalid
			}
			delete(pending, message.ToolCallID)
		} else if message.ToolCallID != "" {
			return invalid
		}
		if len(blocks) == 0 && (message.Role != "assistant" || len(message.ToolCalls) == 0) && message.Role != "tool" {
			return invalid
		}
	}
	if len(pending) > 0 || cacheCount > 4 {
		return invalid
	}
	return nil
}

func hasChatJSON(raw json.RawMessage) bool {
	return len(raw) > 0 && strings.TrimSpace(string(raw)) != "null"
}

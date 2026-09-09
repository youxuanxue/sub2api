package protocolrouter

import (
	"encoding/json"
	"strings"
)

func preservesChatToGemini(req CanonicalRequest) bool {
	if !req.profile.Tools {
		return preservesToGemini(req)
	}
	if req.profile.ContentKinds == 0 || req.profile.ContentKinds&^(ContentText|ContentUnknown) != 0 ||
		req.profile.Continuation != ContinuationNone || req.profile.Reasoning != ReasoningNone || req.profile.PromptCache != PromptCacheNone {
		return false
	}
	var root struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name       string         `json:"name"`
				Parameters map[string]any `json:"parameters"`
				Strict     bool           `json:"strict"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice        json.RawMessage `json:"tool_choice"`
		ParallelToolCalls *bool           `json:"parallel_tool_calls"`
		ResponseFormat    json.RawMessage `json:"response_format"`
		Messages          []struct {
			Role         string          `json:"role"`
			Content      json.RawMessage `json:"content"`
			ToolCallID   string          `json:"tool_call_id"`
			FunctionCall json.RawMessage `json:"function_call"`
			ToolCalls    []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if json.Unmarshal(req.body, &root) != nil || len(root.Tools) == 0 ||
		(root.ParallelToolCalls != nil && !*root.ParallelToolCalls) || hasGeminiJSONValue(root.ResponseFormat) {
		return false
	}
	// The converters carry function calls but do not preserve forced choice,
	// strict JSON-schema validation or disabled parallel calls.
	if hasGeminiJSONValue(root.ToolChoice) {
		var choice string
		if json.Unmarshal(root.ToolChoice, &choice) != nil || choice != "auto" {
			return false
		}
	}
	names := make(map[string]bool, len(root.Tools))
	for _, tool := range root.Tools {
		f := tool.Function
		if tool.Type != "function" || strings.TrimSpace(f.Name) == "" || names[f.Name] || f.Parameters == nil || f.Strict {
			return false
		}
		names[f.Name] = true
	}
	calls := make(map[string]bool)
	for _, message := range root.Messages {
		if hasGeminiJSONValue(message.FunctionCall) {
			return false
		}
		switch message.Role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return false
		}
		if len(message.ToolCalls) > 0 && message.Role != "assistant" {
			return false
		}
		for _, call := range message.ToolCalls {
			var arguments map[string]any
			if call.ID == "" || calls[call.ID] || call.Type != "function" || !names[call.Function.Name] ||
				json.Unmarshal([]byte(call.Function.Arguments), &arguments) != nil || arguments == nil {
				return false
			}
			calls[call.ID] = true
		}
		if message.Role == "tool" {
			if !calls[message.ToolCallID] {
				return false
			}
			delete(calls, message.ToolCallID)
		} else if message.ToolCallID != "" {
			return false
		}
		if !hasGeminiJSONValue(message.Content) {
			if message.Role != "assistant" || len(message.ToolCalls) == 0 {
				return false
			}
		} else if !preservesGeminiFunctionToolContent(message.Content, false) {
			return false
		}
	}
	return true
}

func hasGeminiJSONValue(raw json.RawMessage) bool {
	return len(raw) > 0 && strings.TrimSpace(string(raw)) != "null"
}

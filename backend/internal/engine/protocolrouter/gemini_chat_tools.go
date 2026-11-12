package protocolrouter

import (
	"encoding/json"
	"strings"
)

func preservesChatToGemini(req CanonicalRequest) bool {
	if req.profile.Continuation != ContinuationNone || req.profile.Reasoning == ReasoningSummary {
		return false
	}
	if req.profile.ContentKinds == 0 || req.profile.ContentKinds&^(ContentText|ContentImage|ContentUnknown) != 0 {
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
		Stop              json.RawMessage `json:"stop"`
		FunctionCall      json.RawMessage `json:"function_call"`
		Functions         json.RawMessage `json:"functions"`
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
	if json.Unmarshal(req.body, &root) != nil ||
		(root.ParallelToolCalls != nil && !*root.ParallelToolCalls) ||
		!chatResponseFormatPreserved(root.ResponseFormat) ||
		!chatStopPreserved(root.Stop) ||
		hasGeminiJSONValue(root.FunctionCall) || hasGeminiJSONValue(root.Functions) {
		return false
	}
	if len(root.Tools) == 0 {
		if hasGeminiJSONValue(root.ToolChoice) {
			return false
		}
		for _, message := range root.Messages {
			if (message.Role == "system" || message.Role == "developer") && !geminiSystemTextOnly(message.Content) {
				return false
			}
			if !preservesGeminiFunctionToolContent(message.Content, false) {
				return false
			}
		}
		return true
	}
	names := make(map[string]bool, len(root.Tools))
	builtin := false
	for _, tool := range root.Tools {
		if chatBuiltinTool(tool.Type) {
			builtin = true
			continue
		}
		f := tool.Function
		if (tool.Type != "" && tool.Type != "function") || strings.TrimSpace(f.Name) == "" || names[f.Name] || f.Parameters == nil || f.Strict {
			return false
		}
		names[f.Name] = true
	}
	if builtin && len(names) > 0 {
		return false
	}
	if !chatToolChoicePreserved(root.ToolChoice, names, builtin) {
		return false
	}
	if builtin {
		for _, message := range root.Messages {
			if (message.Role == "system" || message.Role == "developer") && !geminiSystemTextOnly(message.Content) {
				return false
			}
			if len(message.ToolCalls) > 0 || message.Role == "tool" || hasGeminiJSONValue(message.FunctionCall) {
				return false
			}
			if !preservesGeminiFunctionToolContent(message.Content, false) {
				return false
			}
		}
		return true
	}
	calls := make(map[string]bool)
	seenCalls := make(map[string]bool)
	for _, message := range root.Messages {
		if (message.Role == "system" || message.Role == "developer") && !geminiSystemTextOnly(message.Content) {
			return false
		}
		if len(calls) > 0 && message.Role != "tool" {
			return false
		}
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
		seenNames := map[string]bool{}
		for _, call := range message.ToolCalls {
			var arguments map[string]any
			if call.ID == "" || seenCalls[call.ID] || seenNames[call.Function.Name] || (call.Type != "" && call.Type != "function") || !names[call.Function.Name] ||
				json.Unmarshal([]byte(call.Function.Arguments), &arguments) != nil || arguments == nil {
				return false
			}
			seenNames[call.Function.Name] = true
			calls[call.ID] = true
			seenCalls[call.ID] = true
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
	return len(calls) == 0
}

func hasGeminiJSONValue(raw json.RawMessage) bool {
	return len(raw) > 0 && strings.TrimSpace(string(raw)) != "null"
}

func chatBuiltinTool(toolType string) bool {
	switch strings.ToLower(strings.TrimSpace(toolType)) {
	case "web_search", "google_search", "code_execution", "url_context":
		return true
	default:
		return false
	}
}

func chatStopPreserved(raw json.RawMessage) bool {
	if !hasGeminiJSONValue(raw) {
		return true
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return true
	}
	var many []string
	return json.Unmarshal(raw, &many) == nil && len(many) <= 5
}

func chatResponseFormatPreserved(raw json.RawMessage) bool {
	if !hasGeminiJSONValue(raw) {
		return true
	}
	var format struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &format) != nil {
		return false
	}
	switch format.Type {
	case "", "text", "json_object", "json_schema":
		return true
	default:
		return false
	}
}

func chatToolChoicePreserved(raw json.RawMessage, names map[string]bool, builtin bool) bool {
	if !hasGeminiJSONValue(raw) {
		return true
	}
	var choice string
	if json.Unmarshal(raw, &choice) == nil {
		switch choice {
		case "auto", "none":
			return true
		case "required":
			return !builtin && len(names) > 0
		default:
			return false
		}
	}
	if builtin {
		return false
	}
	var object struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &object) != nil || object.Type != "function" {
		return false
	}
	return object.Function.Name != "" && names[object.Function.Name]
}

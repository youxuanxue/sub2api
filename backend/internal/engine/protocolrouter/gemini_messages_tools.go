package protocolrouter

import "encoding/json"

func preservesMessagesToGemini(req CanonicalRequest) bool {
	if !req.profile.Tools {
		return preservesToGemini(req)
	}
	if req.profile.ContentKinds == 0 || req.profile.ContentKinds & ^(ContentText|ContentUnknown) != 0 ||
		req.profile.Continuation != ContinuationNone ||
		req.profile.Reasoning != ReasoningNone ||
		req.profile.PromptCache != PromptCacheNone {
		return false
	}
	// The Messages converter carries standard function declarations, but does
	// not translate forced/disabled tool choice or parallel-call constraints.
	var root struct {
		Tools      []map[string]json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage              `json:"tool_choice"`
		Messages   []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(req.body, &root) != nil || len(root.Tools) == 0 {
		return false
	}
	if len(root.ToolChoice) > 0 && string(root.ToolChoice) != "null" {
		var choice map[string]string
		if json.Unmarshal(root.ToolChoice, &choice) != nil || len(choice) != 1 || choice["type"] != "auto" {
			return false
		}
	}
	for _, tool := range root.Tools {
		var name string
		var schema map[string]any
		if _, typed := tool["type"]; typed {
			return false
		}
		if json.Unmarshal(tool["name"], &name) != nil || name == "" ||
			json.Unmarshal(tool["input_schema"], &schema) != nil || schema == nil {
			return false
		}
	}
	for _, message := range root.Messages {
		if !preservesGeminiFunctionToolContent(message.Content, true) {
			return false
		}
	}
	return true
}

func preservesGeminiFunctionToolContent(raw json.RawMessage, allowTools bool) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return true
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
		case "tool_use":
			if !allowTools {
				return false
			}
		case "tool_result":
			if !allowTools || !preservesGeminiFunctionToolContent(block.Content, false) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

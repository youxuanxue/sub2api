package protocolrouter

import (
	"encoding/json"
	"strings"
)

func preservesMessagesToGemini(req CanonicalRequest) bool {
	if req.profile.Continuation != ContinuationNone || req.profile.Reasoning == ReasoningSummary {
		return false
	}
	if req.profile.ContentKinds == 0 || req.profile.ContentKinds&^(ContentText|ContentImage|ContentUnknown) != 0 {
		return false
	}
	var root struct {
		System     json.RawMessage              `json:"system"`
		Tools      []map[string]json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage              `json:"tool_choice"`
		Messages   []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(req.body, &root) != nil || !geminiSystemTextOnly(root.System) {
		return false
	}
	if !req.profile.Tools {
		if hasGeminiJSONValue(root.ToolChoice) {
			return false
		}
		for _, message := range root.Messages {
			if !preservesGeminiFunctionToolContent(message.Content, false) {
				return false
			}
		}
		return true
	}
	if len(root.Tools) == 0 || !messagesToolChoicePreserved(root.ToolChoice, root.Tools) {
		return false
	}
	names := map[string]bool{}
	for _, tool := range root.Tools {
		var name string
		var schema map[string]any
		if _, typed := tool["type"]; typed {
			return false
		}
		if json.Unmarshal(tool["name"], &name) != nil || name == "" || names[name] ||
			json.Unmarshal(tool["input_schema"], &schema) != nil || schema == nil {
			return false
		}
		names[name] = true
	}
	for _, message := range root.Messages {
		if !preservesGeminiFunctionToolContent(message.Content, true) {
			return false
		}
	}
	return true
}

func messagesToolChoicePreserved(raw json.RawMessage, tools []map[string]json.RawMessage) bool {
	if !hasGeminiJSONValue(raw) {
		return true
	}
	names := map[string]bool{}
	for _, tool := range tools {
		var name string
		if json.Unmarshal(tool["name"], &name) == nil && name != "" {
			names[name] = true
		}
	}
	var choice string
	if json.Unmarshal(raw, &choice) == nil {
		switch choice {
		case "auto", "none", "any", "required":
			return true
		default:
			return false
		}
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	if disabled, ok := object["disable_parallel_tool_use"].(bool); ok && disabled {
		return false
	}
	kind, _ := object["type"].(string)
	switch kind {
	case "", "auto", "none", "any":
		return true
	case "tool":
		name, _ := object["name"].(string)
		return name != "" && names[name]
	default:
		return false
	}
}

func preservesGeminiFunctionToolContent(raw json.RawMessage, allowTools bool) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return true
	}
	var blocks []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking"`
		Content  json.RawMessage `json:"content"`
		Source   struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		switch block.Type {
		case "", "text":
		case "thinking", "reasoning":
			thought := block.Thinking
			if thought == "" {
				thought = block.Text
			}
			if strings.TrimSpace(thought) == "" && block.Type == "thinking" {
				continue
			}
		case "image":
			if block.Source.Type != "base64" || block.Source.Data == "" || !strings.HasPrefix(strings.ToLower(block.Source.MediaType), "image/") {
				return false
			}
		case "image_url":
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(block.ImageURL.URL)), "data:image/") {
				return false
			}
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

func geminiSystemTextOnly(raw json.RawMessage) bool {
	if !hasGeminiJSONValue(raw) {
		return true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return !strings.Contains(strings.ToLower(text), "data:image/")
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		if block.Type != "" && block.Type != "text" {
			return false
		}
	}
	return true
}

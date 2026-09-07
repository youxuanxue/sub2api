package protocolrouter

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseCanonicalRequest is shared by pre-billing candidate evaluation and execution.
func ParseCanonicalRequest(
	inbound Protocol,
	responsesPath ResponsesPathKind,
	model string,
	stream bool,
	body []byte,
) (CanonicalRequest, error) {
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return CanonicalRequest{}, fmt.Errorf("parse canonical protocol request: %w", err)
	}
	root, _ := document.(map[string]any)
	profile := RequestProfile{
		Stream:       stream,
		Tools:        protocolHasTools(root),
		ToolChoice:   protocolToolChoice(root["tool_choice"]),
		Continuation: protocolContinuation(root),
		Reasoning:    protocolReasoning(root),
		PromptCache:  protocolPromptCache(root, document),
		ContentKinds: protocolContentKinds(document),
	}
	if profile.ContentKinds == 0 {
		profile.ContentKinds = ContentText
	}
	return NewCanonicalRequest(CanonicalRequestInput{
		InboundProtocol: inbound,
		RequestedModel:  model,
		ResponsesPath:   responsesPath,
		Profile:         profile,
		Body:            body,
	})
}

func protocolToolChoice(raw any) ToolChoiceKind {
	switch value := raw.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "auto":
			return ToolChoiceAuto
		case "required", "any":
			return ToolChoiceRequired
		case "none", "":
			return ToolChoiceNone
		default:
			return ToolChoiceNamed
		}
	case map[string]any:
		typeName, _ := value["type"].(string)
		switch strings.ToLower(strings.TrimSpace(typeName)) {
		case "auto":
			return ToolChoiceAuto
		case "any", "required":
			return ToolChoiceRequired
		case "none", "":
			if _, named := value["name"]; !named {
				return ToolChoiceNone
			}
		}
		return ToolChoiceNamed
	default:
		return ToolChoiceNone
	}
}

func protocolContinuation(root map[string]any) ContinuationKind {
	if nonEmptyProtocolString(root["previous_response_id"]) {
		return ContinuationPreviousResponse
	}
	if nonEmptyProtocolString(root["conversation"]) || nonEmptyProtocolString(root["conversation_id"]) {
		return ContinuationConversation
	}
	return ContinuationNone
}

func protocolReasoning(root map[string]any) ReasoningKind {
	if reasoning, ok := root["reasoning"].(map[string]any); ok && len(reasoning) > 0 {
		if nonEmptyProtocolString(reasoning["summary"]) {
			return ReasoningSummary
		}
		return ReasoningEffort
	}
	if thinking, ok := root["thinking"].(map[string]any); ok && len(thinking) > 0 {
		return ReasoningEffort
	}
	if nonEmptyProtocolString(root["reasoning_effort"]) {
		return ReasoningEffort
	}
	return ReasoningNone
}

func protocolPromptCache(root map[string]any, document any) PromptCacheKind {
	if nonEmptyProtocolString(root["prompt_cache_key"]) {
		return PromptCacheKey
	}
	if protocolDocumentHasKey(document, "cache_control") {
		return PromptCachePlacement
	}
	return PromptCacheNone
}

func protocolContentKinds(document any) ContentKindSet {
	var kinds ContentKindSet
	var walk func(any, bool)
	walk = func(value any, contentContext bool) {
		switch current := value.(type) {
		case []any:
			for _, item := range current {
				walk(item, contentContext)
			}
		case map[string]any:
			if contentContext {
				rawType, _ := current["type"].(string)
				switch strings.ToLower(strings.TrimSpace(rawType)) {
				case "text", "input_text", "output_text":
					kinds |= ContentText
				case "image", "input_image", "image_url":
					kinds |= ContentImage
				case "audio", "input_audio":
					kinds |= ContentAudio
				case "file", "input_file":
					kinds |= ContentFile
				case "":
					// Message/container objects are classified by their content children.
				default:
					kinds |= ContentUnknown
				}
			}
			for key, child := range current {
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "content", "input", "prompt":
					walk(child, true)
				case "messages":
					walk(child, false)
				}
			}
		case string:
			if contentContext && strings.TrimSpace(current) != "" {
				kinds |= ContentText
			}
		}
	}
	walk(document, false)
	return kinds
}

func protocolHasTools(root map[string]any) bool {
	tools, ok := root["tools"].([]any)
	return ok && len(tools) > 0
}

func protocolDocumentHasKey(document any, key string) bool {
	switch current := document.(type) {
	case []any:
		for _, item := range current {
			if protocolDocumentHasKey(item, key) {
				return true
			}
		}
	case map[string]any:
		if _, ok := current[key]; ok {
			return true
		}
		for _, child := range current {
			if protocolDocumentHasKey(child, key) {
				return true
			}
		}
	}
	return false
}

func nonEmptyProtocolString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

package handler

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

func protocolPlanEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Path == "" {
		return strings.TrimSpace(endpoint)
	}
	return parsed.Path
}

func newCanonicalProtocolRequest(
	inbound protocolrouter.Protocol,
	responsesPath protocolrouter.ResponsesPathKind,
	model string,
	stream bool,
	body []byte,
) (protocolrouter.CanonicalRequest, error) {
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return protocolrouter.CanonicalRequest{}, fmt.Errorf("parse canonical protocol request: %w", err)
	}
	root, _ := document.(map[string]any)
	profile := protocolrouter.RequestProfile{
		Stream:       stream,
		Tools:        protocolHasTools(root),
		ToolChoice:   protocolToolChoice(root["tool_choice"]),
		Continuation: protocolContinuation(root),
		Reasoning:    protocolReasoning(root),
		PromptCache:  protocolPromptCache(root, document),
		ContentKinds: protocolContentKinds(document),
	}
	if profile.ContentKinds == 0 {
		profile.ContentKinds = protocolrouter.ContentText
	}
	return protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: inbound,
		RequestedModel:  model,
		ResponsesPath:   responsesPath,
		Profile:         profile,
		Body:            body,
	})
}

func protocolToolChoice(raw any) protocolrouter.ToolChoiceKind {
	switch value := raw.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "auto":
			return protocolrouter.ToolChoiceAuto
		case "required", "any":
			return protocolrouter.ToolChoiceRequired
		case "none", "":
			return protocolrouter.ToolChoiceNone
		default:
			return protocolrouter.ToolChoiceNamed
		}
	case map[string]any:
		typeName, _ := value["type"].(string)
		switch strings.ToLower(strings.TrimSpace(typeName)) {
		case "auto":
			return protocolrouter.ToolChoiceAuto
		case "any", "required":
			return protocolrouter.ToolChoiceRequired
		case "none", "":
			if _, named := value["name"]; !named {
				return protocolrouter.ToolChoiceNone
			}
		}
		return protocolrouter.ToolChoiceNamed
	default:
		return protocolrouter.ToolChoiceNone
	}
}

func protocolContinuation(root map[string]any) protocolrouter.ContinuationKind {
	if nonEmptyProtocolString(root["previous_response_id"]) {
		return protocolrouter.ContinuationPreviousResponse
	}
	if nonEmptyProtocolString(root["conversation"]) || nonEmptyProtocolString(root["conversation_id"]) {
		return protocolrouter.ContinuationConversation
	}
	return protocolrouter.ContinuationNone
}

func protocolReasoning(root map[string]any) protocolrouter.ReasoningKind {
	if reasoning, ok := root["reasoning"].(map[string]any); ok && len(reasoning) > 0 {
		if nonEmptyProtocolString(reasoning["summary"]) {
			return protocolrouter.ReasoningSummary
		}
		return protocolrouter.ReasoningEffort
	}
	if thinking, ok := root["thinking"].(map[string]any); ok && len(thinking) > 0 {
		return protocolrouter.ReasoningEffort
	}
	if nonEmptyProtocolString(root["reasoning_effort"]) {
		return protocolrouter.ReasoningEffort
	}
	return protocolrouter.ReasoningNone
}

func protocolPromptCache(root map[string]any, document any) protocolrouter.PromptCacheKind {
	if nonEmptyProtocolString(root["prompt_cache_key"]) {
		return protocolrouter.PromptCacheKey
	}
	if protocolDocumentHasKey(document, "cache_control") {
		return protocolrouter.PromptCachePlacement
	}
	return protocolrouter.PromptCacheNone
}

func protocolContentKinds(document any) protocolrouter.ContentKindSet {
	var kinds protocolrouter.ContentKindSet
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
					kinds |= protocolrouter.ContentText
				case "image", "input_image", "image_url":
					kinds |= protocolrouter.ContentImage
				case "audio", "input_audio":
					kinds |= protocolrouter.ContentAudio
				case "file", "input_file":
					kinds |= protocolrouter.ContentFile
				case "":
					// Message/container objects are classified by their content children.
				default:
					kinds |= protocolrouter.ContentUnknown
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
				kinds |= protocolrouter.ContentText
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

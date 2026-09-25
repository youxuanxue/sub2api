package protocolrouter

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ProviderCapability describes provider semantics independently of HTTP/auth transport.
type ProviderCapability string

const (
	ProviderCapabilityGeneral   ProviderCapability = ""
	ProviderCapabilityGeminiWeb ProviderCapability = "gemini_web"
)

func (a AccountSnapshot) permitsProviderRequest(request CanonicalRequest) bool {
	if a.providerCapability != ProviderCapabilityGeminiWeb {
		return true
	}
	return GeminiWebRequestSupported(request, a.imageOutput)
}

// GeminiWebRequestSupported validates provider-effective intent after the Plan
// has applied only authorized budget normalization. Streaming is buffered: the
// Worker emits its completed response as one SSE event.
func GeminiWebRequestSupported(request CanonicalRequest, image bool) bool {
	if request.inboundProtocol == ProtocolGeminiGenerateContent {
		return GeminiWebNativeBodySupported(request.body, image)
	}
	var root map[string]any
	if json.Unmarshal(request.body, &root) != nil {
		return false
	}
	allowed := []string{"model", "stream", "generationConfig"}
	var content any
	switch request.inboundProtocol {
	case ProtocolMessages, ProtocolChatCompletions:
		allowed = append(allowed, "messages")
		messages, ok := root["messages"].([]any)
		if !ok || len(messages) != 1 {
			return false
		}
		turn, ok := messages[0].(map[string]any)
		if !ok || !geminiWebOnlyKeys(turn, "role", "content") || turn["role"] != "user" {
			return false
		}
		content = turn["content"]
	case ProtocolResponses:
		if request.responsesPath != ResponsesPathRoot {
			return false
		}
		allowed = append(allowed, "input")
		switch input := root["input"].(type) {
		case string:
			content = input
		case []any:
			if len(input) != 1 {
				return false
			}
			turn, ok := input[0].(map[string]any)
			if !ok || !geminiWebOnlyKeys(turn, "type", "role", "content") || turn["role"] != "user" {
				return false
			}
			if kind, exists := turn["type"]; exists && kind != "message" {
				return false
			}
			content = turn["content"]
		default:
			return false
		}
	default:
		return false
	}
	if !geminiWebOnlyKeys(root, allowed...) {
		return false
	}
	if stream, exists := root["stream"]; exists {
		if _, ok := stream.(bool); !ok {
			return false
		}
	}
	parts := []any{}
	switch value := content.(type) {
	case string:
		parts = append(parts, map[string]any{"text": value})
	case []any:
		for _, raw := range value {
			part, ok := raw.(map[string]any)
			if !ok || !geminiWebOnlyKeys(part, "type", "text") {
				return false
			}
			kind := "text"
			if request.inboundProtocol == ProtocolResponses {
				kind = "input_text"
			}
			if part["type"] != kind {
				return false
			}
			text, ok := part["text"].(string)
			if !ok {
				return false
			}
			parts = append(parts, map[string]any{"text": text})
		}
	default:
		return false
	}
	native := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": parts}}}
	if config, exists := root["generationConfig"]; exists {
		native["generationConfig"] = config
	}
	body, err := json.Marshal(native)
	return err == nil && GeminiWebNativeBodySupported(body, image)
}

// This is an admission projection of worker.request_prompt, not a converter.
// In particular it never flattens system/history, strips tools, or drops limits.
func GeminiWebNativeBodySupported(body []byte, image bool) bool {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || root == nil || !geminiWebOnlyKeys(root, "contents", "generationConfig") {
		return false
	}
	contents, ok := root["contents"].([]any)
	if !ok || len(contents) != 1 {
		return false
	}
	turn, ok := contents[0].(map[string]any)
	if !ok || !geminiWebOnlyKeys(turn, "role", "parts") {
		return false
	}
	if role, exists := turn["role"]; exists && role != "user" {
		return false
	}
	parts, ok := turn["parts"].([]any)
	if !ok {
		return false
	}
	texts := make([]string, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok || len(part) != 1 {
			return false
		}
		text, ok := part["text"].(string)
		if !ok {
			return false
		}
		texts = append(texts, text)
	}
	prompt := strings.Join(texts, "\n")
	// Python str.strip additionally treats these four separators as whitespace.
	blank := strings.TrimFunc(prompt, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= '\u001c' && r <= '\u001f')
	}) == ""
	if blank || utf8.RuneCountInString(prompt) > 32000 {
		return false
	}
	if raw, exists := root["generationConfig"]; exists {
		config, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		allowedConfigKeys := []string{"responseModalities"}
		if image {
			allowedConfigKeys = append(allowedConfigKeys, "imageConfig")
		}
		if !geminiWebOnlyKeys(config, allowedConfigKeys...) {
			return false
		}
		if raw, exists := config["responseModalities"]; exists {
			modalities, ok := raw.([]any)
			if !ok || len(modalities) == 0 {
				return false
			}
			for _, mode := range modalities {
				if mode != "TEXT" && mode != "IMAGE" {
					return false
				}
			}
			hasImage := false
			for _, mode := range modalities {
				hasImage = hasImage || mode == "IMAGE"
			}
			if hasImage != image {
				return false
			}
		}
		if raw := config["imageConfig"]; raw != nil {
			imageConfig, ok := raw.(map[string]any)
			if !ok || !geminiWebOnlyKeys(imageConfig, "aspectRatio") {
				return false
			}
			ratio, ok := imageConfig["aspectRatio"].(string)
			if !ok || !geminiWebImageAspectRatioSupported(ratio) {
				return false
			}
		}
	}
	return true
}

func geminiWebImageAspectRatioSupported(ratio string) bool {
	switch ratio {
	case "1:1", "9:16", "3:4", "4:3", "16:9":
		return true
	default:
		return false
	}
}

func geminiWebOnlyKeys(object map[string]any, allowed ...string) bool {
	for key := range object {
		found := false
		for _, name := range allowed {
			found = found || key == name
		}
		if !found {
			return false
		}
	}
	return true
}

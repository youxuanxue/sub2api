package service

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

// GeminiWebRelayCredentialKey declares the restricted Worker contract on a
// relay account. Session-bearing edge accounts already declare it via gemini_web.
// Account names, public model aliases and edge hostnames are not capabilities.
const GeminiWebRelayCredentialKey = "gemini_web_relay"

func isGeminiWebAccount(account *Account) bool {
	if account == nil || account.Platform != PlatformGemini || account.Type != AccountTypeAPIKey {
		return false
	}
	_, session := account.Credentials["gemini_web"].(map[string]any)
	relay, _ := account.Credentials[GeminiWebRelayCredentialKey].(bool)
	return session || relay
}

// Gemini API-key accounts retain the native capability owner; they are outside
// protocolrouter's governed account set. Candidate admission and refresh both
// call this projection, before billing or an upstream request. Worker validation
// remains authoritative; shared fixtures check the projection against it.
func geminiWebSupportsRequest(ctx context.Context, account *Account, model string, shape UniversalShape) bool {
	if !isGeminiWebAccount(account) || shape == ShapeSkip {
		return true
	}
	if shape == ShapeAnthropicCountTokens {
		// This endpoint is served locally and never invokes the Worker.
		return shouldEstimateCountTokensLocally(account)
	}
	if shape != ShapeGemini {
		// Chat/Responses conversion supplies maxOutputTokens even when omitted by
		// the client. Messages requires max_tokens. None is a Worker capability;
		// do not discard those controls to force a compatible-looking request.
		return false
	}
	var body []byte
	if candidate := CandidateRequestFromContext(ctx); candidate != nil {
		// ShapeGemini also includes countTokens, which the Worker does not serve.
		if !strings.HasSuffix(candidate.path, ":generateContent") && !strings.HasSuffix(candidate.path, ":streamGenerateContent") {
			return false
		}
		// Native image requests intentionally bypass the text Plan context.
		body = candidate.body
	} else if request, ok := protocolRoutingCanonicalRequest(ctx); ok && request.InboundProtocol() == protocolrouter.ProtocolGeminiGenerateContent {
		body = request.Body()
	}
	return geminiWebNativeBodySupported(body, isImageGenerationModel(account.GetMappedModel(model)))
}

// This is an admission projection of worker.request_prompt, not a converter.
// In particular it never flattens system/history, strips tools, or drops limits.
func geminiWebNativeBodySupported(body []byte, image bool) bool {
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
		if !ok || !geminiWebOnlyKeys(config, "responseModalities") {
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
	}
	return true
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

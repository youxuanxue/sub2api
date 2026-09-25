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

// isGeminiWebEdgeRelayStub identifies the prod-side Gemini Web relay. The
// explicit relay marker is the capability boundary: a local Worker account may
// carry gemini_web runtime state, while the prod stub carries only the edge
// reference and must never be treated as an import target.
func isGeminiWebEdgeRelayStub(account *Account) bool {
	if account == nil || account.Platform != PlatformGemini || account.Type != AccountTypeAPIKey {
		return false
	}
	relayKind, _ := account.Extra["relay_kind"].(string)
	if strings.TrimSpace(relayKind) != "gemini_web" {
		return false
	}
	relay, ok := account.Credentials[GeminiWebRelayCredentialKey].(bool)
	return ok && relay && isEdgeMirrorStub(account, edgeIDPattern)
}

// CanImportGeminiWebSession accepts local Gemini Web Worker declarations with
// or without a runtime. An empty gemini_web object is the copied account's
// explicit capability declaration; the runtime itself remains the binding SSOT.
// Production relays reference an edge account; importing into their database
// would not update that edge's session. Names, URLs and model aliases are not identity.
func CanImportGeminiWebSession(account *Account) bool {
	if account == nil || account.Platform != PlatformGemini || account.Type != AccountTypeAPIKey {
		return false
	}
	if relay, exists := account.Credentials[GeminiWebRelayCredentialKey]; exists && relay != false {
		return false
	}
	if account.Extra["relay_kind"] == "gemini_web" {
		return false
	}
	web, declared := account.Credentials["gemini_web"].(map[string]any)
	if !declared || web == nil {
		return false
	}
	if _, exists := web["runtime"]; exists {
		return geminiWebRuntimeBound(account)
	}
	return !account.Schedulable
}

func geminiWebRuntimeBound(account *Account) bool {
	web, _ := account.Credentials["gemini_web"].(map[string]any)
	runtime, _ := web["runtime"].(map[string]any)
	return runtime != nil
}

func isGeminiWebAccount(account *Account) bool {
	if account == nil || account.Platform != PlatformGemini || account.Type != AccountTypeAPIKey {
		return false
	}
	_, session := account.Credentials["gemini_web"].(map[string]any)
	relay, _ := account.Credentials[GeminiWebRelayCredentialKey].(bool)
	return session || relay
}

// Local Gemini Web Worker accounts retain their native capability owner.
// Production relay stubs are projected into protocolrouter above and are
// admitted here only as the native-body fallback. Candidate admission and
// refresh both call this projection before billing or an upstream request.
func geminiWebSupportsRequest(ctx context.Context, account *Account, model string, shape UniversalShape) bool {
	if !isGeminiWebAccount(account) {
		return true
	}
	relay, _ := account.Credentials[GeminiWebRelayCredentialKey].(bool)
	if isGeminiWebEdgeRelayStub(account) {
		// The prod relay is governed by protocolrouter. Messages/Chat/Responses
		// are converted there and execute through the native Gemini hop; only the
		// native Gemini body contract remains owned by the Worker admission gate.
		if shape != ShapeGemini {
			return true
		}
	}
	if !relay && !geminiWebRuntimeBound(account) {
		return false
	}
	if shape == ShapeSkip {
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

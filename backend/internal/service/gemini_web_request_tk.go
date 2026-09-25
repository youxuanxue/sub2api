package service

import (
	"context"
	"strings"
)

// GeminiWebRelayCredentialKey declares the restricted Worker contract on a
// relay account. Session-bearing edge accounts already declare it via gemini_web.
// Account names, public model aliases and edge hostnames are not capabilities.
const GeminiWebRelayCredentialKey = "gemini_web_relay"

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

// Lifecycle and locally served endpoints are checked here. Request legality is
// evaluated only by protocolrouter.Plan against the original client body.
func geminiWebSupportsRequest(ctx context.Context, account *Account, model string, shape UniversalShape) bool {
	if !isGeminiWebAccount(account) {
		return true
	}
	relay, _ := account.Credentials[GeminiWebRelayCredentialKey].(bool)
	if !relay && !geminiWebRuntimeBound(account) {
		return false
	}
	if shape == ShapeSkip {
		return true
	}
	if shape == ShapeAnthropicCountTokens {
		return shouldEstimateCountTokensLocally(account)
	}
	if shape == ShapeGemini {
		if candidate := CandidateRequestFromContext(ctx); candidate != nil &&
			!strings.HasSuffix(candidate.path, ":generateContent") && !strings.HasSuffix(candidate.path, ":streamGenerateContent") {
			return false
		}
	}
	_, ok := protocolRoutingCanonicalRequest(ctx)
	return ok
}

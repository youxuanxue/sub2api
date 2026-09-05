package service

import (
	"errors"
	"net/http"
	"strings"
)

// AccountUsesOfficialOpenAIUpstream reports whether this account's stored
// credential may be sent to api.openai.com native endpoints
// (/v1/responses, /v1/responses/input_tokens, ...).
//
// This is the legacy safety predicate for an explicitly configured official
// OpenAI destination. Only platform=openai credentials whose stored base_url
// names api.openai.com qualify. Configurable accounts with an empty URL,
// NewAPI channels, CN providers, CloudWise/tokensea relays, and every other
// platform default to false (fail closed). Governed routing remains owned by
// protocolrouter's immutable plan and OfficialEndpointProfile.
func AccountUsesOfficialOpenAIUpstream(account *Account) bool {
	if account == nil || account.Platform != PlatformOpenAI {
		return false
	}
	switch account.Type {
	case AccountTypeOAuth:
		return true
	case AccountTypeAPIKey:
		baseURL := strings.TrimSpace(account.GetCredential("base_url"))
		return baseURL != "" && isOfficialOpenAIModelsBaseURL(baseURL)
	default:
		return false
	}
}

// AccountShouldLocalEstimateCountTokens reports whether /v1/messages/count_tokens
// must return a local tiktoken estimate and must not open an upstream HTTP
// request. Derived from AccountUsesOfficialOpenAIUpstream: foreign credentials
// with no dedicated native input_tokens URL (or CN providers, whose hosts do
// not implement that endpoint) stay local.
func AccountShouldLocalEstimateCountTokens(account *Account) bool {
	if account == nil || AccountUsesOfficialOpenAIUpstream(account) {
		return false
	}
	if account.Platform == PlatformGrok {
		return false
	}
	if account.IsCNProvider() {
		return true
	}
	// Qianfan Token Plan Person has no OpenAI Responses input_tokens surface;
	// nativeOpenAIBaseURLForAccount is non-empty for chat, so without this
	// branch count_tokens would invent .../tokenplan/personal/v1/responses/input_tokens.
	if isNewAPIQianfanTokenPlanAccount(account) {
		return true
	}
	return strings.TrimSpace(nativeOpenAIBaseURLForAccount(account)) == ""
}

// IsOfficialOpenAIAPIKeyHelpText reports OpenAI's official 401 body that names
// platform.openai.com/account/api-keys. When this text arrives for a foreign
// credential, it is a gateway routing defect, not evidence the credential died.
func IsOfficialOpenAIAPIKeyHelpText(statusCode int, body []byte) bool {
	if statusCode != http.StatusUnauthorized {
		return false
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "incorrect api key provided") &&
		strings.Contains(lower, "platform.openai.com/account/api-keys")
}

// IsForeignCredentialOfficialOpenAIReject is the account-health skip predicate:
// an official OpenAI API-key help page came back for a credential that must
// never be sent to api.openai.com.
func IsForeignCredentialOfficialOpenAIReject(account *Account, statusCode int, body []byte) bool {
	return !AccountUsesOfficialOpenAIUpstream(account) &&
		IsOfficialOpenAIAPIKeyHelpText(statusCode, body)
}

// ErrForeignCredentialOfficialOpenAIFallback is returned instead of silently
// defaulting an unresolved upstream to api.openai.com. The native
// OpenAI-compatible forwarders resolve their base URL from OpenAI-family
// accessors, which return "" for foreign platforms (newapi channels, ...);
// turning that "" into the official host POSTs a DashScope/Ark key to OpenAI
// and yields "Incorrect API key provided" — a routing defect that also looks
// like a dead credential. Fail closed instead.
var ErrForeignCredentialOfficialOpenAIFallback = errors.New(
	"refusing unresolved custom account fallback to api.openai.com: explicit base_url is required")

// OfficialOpenAIFallbackAllowed reports whether an unresolved (empty) base URL
// may fall back to the official OpenAI host for this account.
func OfficialOpenAIFallbackAllowed(account *Account) bool {
	return AccountUsesOfficialOpenAIUpstream(account)
}

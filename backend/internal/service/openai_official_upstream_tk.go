package service

import (
	"net/http"
	"strings"
)

// AccountUsesOfficialOpenAIUpstream reports whether this account's stored
// credential may be sent to api.openai.com native endpoints
// (/v1/responses, /v1/responses/input_tokens, ...).
//
// This is the single owner for "official OpenAI host" decisions. Only
// platform=openai credentials whose base_url is empty or api.openai.com
// qualify. NewAPI channels, CN providers, CloudWise/tokensea relays, and
// every other platform are foreign credentials — adding a platform or
// channel type defaults to false (fail closed).
func AccountUsesOfficialOpenAIUpstream(account *Account) bool {
	if account == nil || account.Platform != PlatformOpenAI {
		return false
	}
	switch account.Type {
	case AccountTypeOAuth:
		return true
	case AccountTypeAPIKey:
		baseURL := strings.TrimSpace(account.GetCredential("base_url"))
		return baseURL == "" || isOfficialOpenAIModelsBaseURL(baseURL)
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

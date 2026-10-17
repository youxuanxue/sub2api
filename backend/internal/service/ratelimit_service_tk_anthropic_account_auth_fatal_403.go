package service

import "strings"

// tkAnthropicAccountAuthFatal403Keywords match structured Anthropic 403 bodies
// that unambiguously signal broken account credentials — not model/request-level
// denials. Precision bar mirrors #810: false positives permanently disable.
var tkAnthropicAccountAuthFatal403Keywords = []string{
	"invalid bearer token",
	"invalid x-api-key",
	"oauth token lacks required scopes",
}

func tkIsAnthropicAccountAuthFatal403(upstreamMsg string, responseBody []byte) bool {
	haystack := strings.ToLower(strings.TrimSpace(upstreamMsg) + " " + string(responseBody))
	if haystack == "" {
		return false
	}
	// Model/request-level denials — healthy account, wrong model or client request.
	if strings.Contains(haystack, "do not have access to this model") {
		return false
	}
	if strings.Contains(haystack, "is not allowed for this organization") {
		return false
	}
	if strings.Contains(haystack, `"type":"upstream_error"`) ||
		strings.Contains(haystack, "upstream request failed") {
		return false
	}
	return matchTempUnschedKeyword(haystack, tkAnthropicAccountAuthFatal403Keywords) != ""
}

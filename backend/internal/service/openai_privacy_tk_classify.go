package service

import "strings"

// classifyOpenAIPrivacyResponse maps a chatgpt.com settings-PATCH response to a privacy_mode.
//
// TK fix: the OpenAI/Cloudflare anti-bot interstitial served on
// chatgpt.com/backend-api/* is an OpenAI-branded challenge page (gray logo SVG +
// <meta http-equiv="refresh">) that does NOT contain the literal "cloudflare" /
// "cf-" / "Just a moment" markers the original heuristic looked for. From a
// datacenter egress IP these challenges are routine, so they were mis-recorded as
// PrivacyModeFailed (red "Fail" on the account card) instead of PrivacyModeCFBlocked
// (yellow "CF" — blocked, retryable, not an account fault). A JSON settings API that
// answers a 403/503 with an HTML body is an anti-bot challenge, never a genuine
// setting failure, so classify it as CF-blocked.
func classifyOpenAIPrivacyResponse(statusCode int, contentType, body string) string {
	if statusCode >= 200 && statusCode < 300 {
		return PrivacyModeTrainingOff
	}
	if (statusCode == 403 || statusCode == 503) && isAntiBotChallenge(contentType, body) {
		return PrivacyModeCFBlocked
	}
	return PrivacyModeFailed
}

// isAntiBotChallenge reports whether a response looks like a Cloudflare / OpenAI
// anti-bot interstitial rather than a real JSON API error.
func isAntiBotChallenge(contentType, body string) bool {
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		return true
	}
	lc := strings.ToLower(body)
	trimmed := strings.TrimSpace(lc)
	if strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html") {
		return true
	}
	for _, marker := range []string{
		"cloudflare",
		"cf-ray",
		"cf-",
		"just a moment",
		`http-equiv="refresh"`,
		"enable-javascript",
		"__cf_",
	} {
		if strings.Contains(lc, marker) {
			return true
		}
	}
	return false
}

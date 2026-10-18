package service

import (
	"log/slog"
	"net/http"
	"strings"
)

// tkIsAnthropicNonAuthoritative429 reports whether an Anthropic 429 lacks ANY
// authoritative anthropic-ratelimit-* signal in its response headers. Pure; no
// settings, no I/O. The caller must already have established statusCode==429 and
// account.Platform==PlatformAnthropic.
func tkIsAnthropicNonAuthoritative429(headers http.Header, responseBody []byte) bool {
	// Extra-usage 429s are header-less too but have a dedicated skip in handle429
	// (isAnthropicExtraUsage429); leave them to that path, don't fold them in here.
	if isAnthropicExtraUsage429(responseBody) {
		return false
	}
	if headers == nil {
		return true
	}
	// Per-window 5h/7d headers — the authoritative source handle429 prefers.
	if calculateAnthropic429ResetTime(headers) != nil {
		return false
	}
	// Any anthropic-ratelimit-* header (legacy aggregate reset, utilization,
	// surpassed-threshold, …) ⇒ authoritative; be conservative and only suppress
	// when there is genuinely no rate-limit signal from the provider.
	for k := range headers {
		if strings.HasPrefix(strings.ToLower(k), "anthropic-ratelimit-") {
			return false
		}
	}
	return true
}

// tkLogAnthropicNonAuthoritative429Skip is the single observability anchor for the
// skip (mirrors the sibling capacity-envelope skips' slog lines).
func tkLogAnthropicNonAuthoritative429Skip(account *Account, statusCode int) {
	slog.Info("anthropic_non_authoritative_429_skip_penalty",
		"account_id", account.ID,
		"status_code", statusCode,
		"pool_mode", account.IsPoolMode())
}

package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

// Non-authoritative Anthropic 429 classification.
//
// A genuine Anthropic account window-limit (5h/7d) ALWAYS carries the
// anthropic-ratelimit-* response headers (per-window reset/utilization, or the
// legacy aggregate anthropic-ratelimit-unified-reset) — that is exactly what
// calculateAnthropic429ResetTime / handle429 read to write an authoritative,
// precise cooldown. A 429 that carries NONE of those headers is therefore not an
// authoritative account window-limit ("真窗口限流一定带头").
//
// In TokenKey's prod→edge mirror topology the header-less 429s are the edge's own
// capacity / failover envelopes relayed back to a prod cc-<edge> stub:
//
//   - "No available accounts: no available accounts"      (empty-pool fast-fail, 429) → tkSkipDownstreamNoAvailableAccountsPenalty
//   - "all available accounts exhausted"                  (failover-exhausted, 502)   → tkSkipDownstreamFailoverExhaustedPenalty
//   - "Upstream rate limit exceeded, please retry later"  (rate-limit failover-       → NEITHER needle matched → fell through and
//                                                          exhausted, 429)               advanced the per-account anthropic_upstream_error
//                                                                                        3/3 ladder, cooling a HEALTHY mirror stub
//                                                                                        (cc-us7, 2026-06-13 23:11: tier-0 30s).
//
// The first two are caught by needle-based skips (ratelimit_service_tk_downstream_
// no_available.go); the third (and any other header-less provider 429) slipped
// through. Crucially pool_mode does NOT exempt anthropic from the cooldown — the
// HandleUpstreamError pool-mode skip carries `&& Platform != PlatformAnthropic`, and
// the pool-mode same-account retries (RetryableOnSameAccount) re-hit the same stub
// each feeding the ladder, so one request can self-trip the 3/3 ladder.
//
// This header-presence classifier generalises the needle skips: no
// anthropic-ratelimit-* headers ⇒ non-authoritative ⇒ fail over to the next stub
// without a fallback cooldown or ladder advance. A real relayed window-limit still
// carries the headers and is unaffected. Retry-After is deliberately NOT treated as
// authoritative: TokenKey's own capacity envelopes (and the empty-pool fast-fail)
// set Retry-After:5, so keying on it would mis-classify the very envelopes above.

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

// tkSkipAnthropicNonAuthoritative429 is true when a 429 should fail over WITHOUT
// any account penalty (no handle429 fallback cooldown, no anthropic_upstream_error
// 3/3 ladder advance) because it carries no authoritative anthropic-ratelimit-*
// headers. Gated by SettingKeyAnthropicNonAuthoritative429Failover (default on);
// an operator on a direct (non-mirror) Anthropic deployment can disable it to keep
// the conservative 5s fallback cooldown. Caller ensures Platform==anthropic.
func (s *RateLimitService) tkSkipAnthropicNonAuthoritative429(ctx context.Context, headers http.Header, responseBody []byte) bool {
	if !tkIsAnthropicNonAuthoritative429(headers, responseBody) {
		return false
	}
	if s.settingService != nil && !s.settingService.IsAnthropicNonAuthoritative429FailoverEnabled(ctx) {
		return false
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

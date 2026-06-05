package service

import (
	"net/http"
	"strings"
)

// tkIsDownstreamNoAvailableAccounts reports whether an Anthropic upstream error
// is the downstream gateway's own "no available accounts" pool-exhaustion signal
// rather than a health problem with the (stub) account that forwarded the
// request.
//
// TK (prod incident 2026-05-31): on the prod main gateway, Anthropic is reached
// only through per-edge api-key mirror "stub" accounts (cc-us1 / cc-uk1 / …) that
// forward to the corresponding edge. When a thin edge pool (e.g. uk1 has a single
// schedulable OAuth account) briefly saturates on a burst of parallel Claude Code
// haiku background requests, the edge returns a 503 or 429 (since 2026-06 empty-
// pool fast-fail uses 429+Retry-After; see handler/no_available_accounts_tk.go)
// whose body is *our own* gateway error envelope:
// {"type":"error","error":{"type":"api_error",
// "message":"No available accounts: no available accounts"}}.
//
// That response is a transient *downstream* capacity blip — the forwarding stub itself
// is perfectly healthy. Counting it toward the per-account anthropic_upstream_error
// threshold (handleAnthropicUpstreamErrorWithOptions) lets a 3-request edge burst
// trip the 3/3 ladder and cool the whole edge stub for 10 minutes
// (SetTempUnschedulable, tier=2). With both regional stubs cooled at once the prod
// pool collapses and every model (opus + haiku) 503s — a self-inflicted 503
// amplifier. The in-flight request should instead fail over to the next stub
// (another edge), leaving stub health untouched.
//
// The phrase "no available accounts" is TokenKey's own internal scheduler string
// (service.ErrNoAvailableAccounts, rendered by the gateway handlers as an
// "api_error"); Anthropic's real API never emits it, so a case-insensitive match
// on the body is a specific signal of downstream pool exhaustion (status may be
// 503 legacy or 429 current empty-pool fast-fail).
func tkIsDownstreamNoAvailableAccounts(upstreamMsg string, responseBody []byte) bool {
	needle := ErrNoAvailableAccounts.Error() // "no available accounts"
	if strings.Contains(strings.ToLower(upstreamMsg), needle) {
		return true
	}
	return strings.Contains(strings.ToLower(string(responseBody)), needle)
}

// tkSkipDownstreamNoAvailableAccountsPenalty is true when an Anthropic upstream
// response is our own downstream gateway's empty-pool signal (503 or 429). Such
// hits must fail over without handle429 cooldown, anthropic_upstream_error ladder
// writes, or Feishu digest "限流冷却（429）" — the stub is healthy; the
// forwarded-to edge pool is empty or saturated.
func tkSkipDownstreamNoAvailableAccountsPenalty(statusCode int, upstreamMsg string, responseBody []byte) bool {
	if statusCode != http.StatusServiceUnavailable && statusCode != http.StatusTooManyRequests {
		return false
	}
	return tkIsDownstreamNoAvailableAccounts(upstreamMsg, responseBody)
}

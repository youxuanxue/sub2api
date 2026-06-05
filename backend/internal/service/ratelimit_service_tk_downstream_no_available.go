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

// tkDownstreamAllAccountsExhaustedNeedle is TokenKey's own failover-exhausted
// client message (handler.handleCCFailoverExhausted / handleResponsesFailoverExhausted
// and the chat/responses select-failure fallbacks), emitted as "server_error" /
// "upstream_error" with HTTP 502 when a downstream gateway's failover loop ran out
// of candidates. Like ErrNoAvailableAccounts it is a TokenKey-generated string the
// real provider APIs never emit.
const tkDownstreamAllAccountsExhaustedNeedle = "all available accounts exhausted"

// tkIsDownstreamAllAccountsExhausted reports whether an error is the downstream
// gateway's own failover-exhausted capacity signal — the sibling of
// tkIsDownstreamNoAvailableAccounts. "no available accounts" means the downstream
// pool had nothing schedulable to even try; "all available accounts exhausted"
// means its failover loop tried every candidate and they were all (transiently)
// unavailable. Both are TokenKey-internal *downstream capacity* signals, not a
// health problem with THIS forwarding stub and not a genuine provider error.
func tkIsDownstreamAllAccountsExhausted(upstreamMsg string, responseBody []byte) bool {
	if strings.Contains(strings.ToLower(upstreamMsg), tkDownstreamAllAccountsExhaustedNeedle) {
		return true
	}
	return strings.Contains(strings.ToLower(string(responseBody)), tkDownstreamAllAccountsExhaustedNeedle)
}

// tkSkipDownstreamFailoverExhaustedPenalty (TK — G2, narrow) generalises the
// no-available skip to the second downstream capacity envelope. It is the only
// addition over the pre-existing no-available skip: a forwarding stub that
// receives the downstream gateway's own "all available accounts exhausted" signal
// must fail over to the next stub without advancing the per-account
// anthropic_upstream_error 3/3 ladder — the stub itself is healthy; the
// forwarded-to edge pool transiently ran dry.
//
// Status set is deliberately wider than the no-available skip (429 OR any 5xx):
// the failover-exhausted envelope is written as HTTP 502, whereas the empty-pool
// fast-fail is 429 (or legacy 503). Crucially this matches ONLY on TokenKey's own
// capacity string — a raw edge-infra 5xx (Caddy 502 HTML, connection reset) or a
// genuine provider error carries no such phrase, so it still flows through the
// tiered cooldown and keeps the beneficial "route away from a broken edge"
// behaviour (PR #333 / 2026-05-21). This is the explicit scope chosen for G2:
// exempt TokenKey downstream *capacity* signals, never genuine failures.
func tkSkipDownstreamFailoverExhaustedPenalty(statusCode int, upstreamMsg string, responseBody []byte) bool {
	if statusCode != http.StatusTooManyRequests && statusCode < 500 {
		return false
	}
	return tkIsDownstreamAllAccountsExhausted(upstreamMsg, responseBody)
}

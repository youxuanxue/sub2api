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

// tkSkipDownstreamFailoverExhaustedPenalty (TK — G2, narrow) generalises the
// no-available skip to the second downstream capacity envelope. It is the only
// addition over the pre-existing no-available skip: a forwarding stub that
// receives the downstream gateway's own failover-terminal signal must fail over
// to the next stub without advancing the per-account anthropic_upstream_error
// ladder. The forwarding stub did not originate the downstream failure.
//
// Status set is deliberately wider than the no-available skip (429 OR any 5xx):
// the failover-terminal envelope is normally written as HTTP 502, whereas the
// empty-pool fast-fail is 429 (or legacy 503). Crucially this matches ONLY
// TokenKey's exact current/legacy client messages — a raw edge-infra 5xx (Caddy
// 502 HTML, connection reset) or a genuine provider error still flows through
// the tiered cooldown and keeps the beneficial route-away behavior.
func tkSkipDownstreamFailoverExhaustedPenalty(statusCode int, upstreamMsg string, responseBody []byte) bool {
	if statusCode != http.StatusTooManyRequests && statusCode < 500 {
		return false
	}
	return IsGatewayFailoverMessage(upstreamMsg, responseBody)
}

// tkIsKiroMirrorStub reports whether this local account is a prod Anthropic
// api-key relay stub that represents the downstream edge Kiro pool.
func tkIsKiroMirrorStub(account *Account) bool {
	if account == nil || account.Platform != PlatformAnthropic || account.Type != AccountTypeAPIKey {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(account.GetCredential("mirror_platform")), string(PlatformKiro))
}

const tkDownstreamKiroServiceUnavailableMessage = "upstream service temporarily unavailable"

// tkSkipDownstreamKiroServiceUnavailablePenalty identifies the stable TokenKey
// 502 envelope emitted by an edge after its Kiro request path failed upstream.
// The prod account is only a relay stub; cooling it repeats the downstream
// failure at the prod layer and can remove every otherwise healthy Kiro route.
// Keep raw proxy/infra 502s eligible for the stub-health fuse by requiring the
// parsed application message to match exactly.
func tkSkipDownstreamKiroServiceUnavailablePenalty(account *Account, statusCode int, upstreamMsg string, responseBody []byte) bool {
	if statusCode != http.StatusBadGateway || !tkIsKiroMirrorStub(account) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(upstreamMsg), tkDownstreamKiroServiceUnavailableMessage) {
		return true
	}
	bodyMsg := extractUpstreamErrorMessage(responseBody)
	return strings.EqualFold(strings.TrimSpace(bodyMsg), tkDownstreamKiroServiceUnavailableMessage)
}

// tkSkipDownstreamKiroOAuthAuthRejectPenalty identifies a Kiro edge OAuth auth
// rejection that crossed the prod mirror boundary as an Anthropic api-key error.
// The prod stub is only a relay; it has no Kiro OAuth token to refresh. The edge
// owns recovery via kiro_oauth_auth_reject_force_refresh_recovered, so
// permanently SetError'ing the prod stub would strand a healthy edge behind a
// stale mirror error.
//
// Scope is intentionally narrow: only Kiro mirror stubs and only the TokenKey
// downstream wrapper / Kiro Invalid bearer shape. 401 keeps the existing wrapper
// compatibility; 403 mirrors the edge-local #970 force-refresh predicate and
// requires the invalid-bearer signal. Real Anthropic api-key 401s, generic
// Anthropic 403s, and other mirror platforms keep the existing hard-disable /
// ladder behaviour.
func tkSkipDownstreamKiroOAuthAuthRejectPenalty(account *Account, statusCode int, upstreamMsg string, responseBody []byte) bool {
	if (statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden) || !tkIsKiroMirrorStub(account) {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(upstreamMsg))
	body := strings.ToLower(string(responseBody))
	if strings.Contains(msg, "invalid bearer token") || strings.Contains(body, "invalid bearer token") {
		return true
	}
	if statusCode == http.StatusForbidden {
		return false
	}
	if strings.Contains(msg, "upstream request failed") || strings.Contains(body, "upstream request failed") {
		return true
	}
	return false
}

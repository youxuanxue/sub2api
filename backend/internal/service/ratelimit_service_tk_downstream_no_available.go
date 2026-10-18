package service

import (
	"net/http"
	"strings"
)

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
// upstream gateway pool is empty or saturated.
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
// TokenKey's exact current/legacy client messages — a raw infrastructure 5xx (Caddy
// 502 HTML, connection reset) or a genuine provider error still flows through
// the tiered cooldown and keeps the beneficial route-away behavior.
func tkSkipDownstreamFailoverExhaustedPenalty(statusCode int, upstreamMsg string, responseBody []byte) bool {
	if statusCode != http.StatusTooManyRequests && statusCode < 500 {
		return false
	}
	return IsGatewayFailoverMessage(upstreamMsg, responseBody)
}

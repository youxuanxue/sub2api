package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// Request-owned Anthropic 429 classification + same-text failover circuit
// breaker.
//
// Incident (prod 2026-06-11 06:44–06:59 UTC): a single >200k long-context
// request on a no-credits subscription account drew a DETERMINISTIC policy 429
// from Anthropic — "Usage credits are required for long context requests." —
// which is a property of the REQUEST, not of any account. Failover fanned the
// same request across all 7 cc-<edge> mirror accounts, every hop fed the
// anthropic 3/3 error ladder, and within 4 minutes the whole mirror pool was
// locked in tier-2 10-minute cooldowns while the edges themselves were already
// healthy again. Two layers of defense, both implemented here:
//
//  1. Known-phrase classifier (tkIsAnthropicRequestOwned429Message): a policy
//     429 is short-circuited on the FIRST hop — the original upstream body is
//     passed through to the caller (so a prod mirror hop can re-classify the
//     same phrase), no account penalty, no failover.
//  2. Same-text circuit breaker (tkAnthropic429SameTextThreshold): for future
//     deterministic 429 variants we cannot enumerate, identical 429 message
//     text from N distinct accounts within ONE request is judged
//     request-owned — stop the fan-out, skip side effects from that hop on.
//     Legitimate per-account rate limits rarely repeat the exact same body
//     text N times in a single failover chain; genuine multi-account burn is
//     surfaced to the client as the 429 it is (with the real upstream text)
//     instead of poisoning the remaining pool.
//
// Body passthrough is load-bearing: the prod ↔ edge mirror topology relays the
// edge's response body to prod, so preserving the original phrase is what lets
// the SAME classifier fire again on the prod hop (the edge→prod boundary used
// to rewrite 429s to a generic text the exemption predicates cannot see).
var tkAnthropicRequestOwned429Markers = []string{
	"usage credits are required",
}

// tkAnthropic429SameTextThreshold is the number of occurrences of an
// IDENTICAL upstream 429 message text, within one request's failover chain,
// at which the 429 is judged deterministic/request-owned. The first
// (threshold-1) hops keep normal semantics (penalty + failover) so genuine
// single-account rate limits are unaffected.
const tkAnthropic429SameTextThreshold = 3

// tkAnthropic429TextSeenContextKey holds the per-request map of normalized
// 429 message text → occurrence count, written only by the sequential
// failover chain of a single request.
const tkAnthropic429TextSeenContextKey = "tk_anthropic_429_text_seen"

// tkIsAnthropicRequestOwned429Message reports whether an upstream 429 message
// identifies a deterministic, request-owned policy rejection. Markers are
// matched case-insensitively and kept tight to avoid matching genuine
// account-level rate-limit texts.
func tkIsAnthropicRequestOwned429Message(upstreamMsg string, responseBody []byte) bool {
	hay := strings.ToLower(upstreamMsg)
	if len(responseBody) > 0 {
		hay += " " + strings.ToLower(string(responseBody))
	}
	for _, m := range tkAnthropicRequestOwned429Markers {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}

// tkNoteAnthropic429Text records one occurrence of the normalized 429 text for
// this request and returns the total occurrence count including this one.
func tkNoteAnthropic429Text(c *gin.Context, normalizedMsg string) int {
	if c == nil || normalizedMsg == "" {
		return 0
	}
	var seen map[string]int
	if v, ok := c.Get(tkAnthropic429TextSeenContextKey); ok {
		seen, _ = v.(map[string]int)
	}
	if seen == nil {
		seen = make(map[string]int, 2)
	}
	seen[normalizedMsg]++
	c.Set(tkAnthropic429TextSeenContextKey, seen)
	return seen[normalizedMsg]
}

// tkHandleAnthropicRequestOwned429 short-circuits a request-owned anthropic
// 429: it writes the ORIGINAL upstream status + body through to the client
// (preserving the policy phrase for the next mirror hop), records ops
// telemetry, and returns handled=true so the caller skips account side
// effects and failover. Returns handled=false for everything else.
//
// Call sites are the anthropic failover branches of Forward /
// forwardAnthropicAPIKeyPassthroughWithInput, after the error body has been
// read and BEFORE handleFailoverSideEffects / handleRetryExhaustedSideEffects.
func (s *GatewayService) tkHandleAnthropicRequestOwned429(c *gin.Context, account *Account, resp *http.Response, respBody []byte) (*ForwardResult, error, bool) {
	if c == nil || account == nil || resp == nil {
		return nil, nil, false
	}
	if resp.StatusCode != http.StatusTooManyRequests || account.Platform != PlatformAnthropic {
		return nil, nil, false
	}
	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	normalized := strings.ToLower(upstreamMsg)

	known := tkIsAnthropicRequestOwned429Message(upstreamMsg, respBody)
	if !known {
		// TK capacity envelopes are exempt from the same-text breaker: a prod
		// mirror hop receiving "No available accounts" (#575 fast-fail) or the
		// failover-exhausted rewrite "Upstream rate limit exceeded, please
		// retry later" from several edges is seeing EDGE-scoped capacity
		// signals — cross-edge failover is the designed remedy there, and the
		// texts are identical by construction, not because the request is
		// poisoned. The breaker stays focused on provider-originated
		// deterministic texts.
		if tkSkipDownstreamNoAvailableAccountsPenalty(resp.StatusCode, upstreamMsg, respBody) ||
			tkSkipDownstreamFailoverExhaustedPenalty(resp.StatusCode, upstreamMsg, respBody) ||
			strings.Contains(normalized, "upstream rate limit exceeded") {
			return nil, nil, false
		}
	}
	occurrences := tkNoteAnthropic429Text(c, normalized)
	if !known && occurrences < tkAnthropic429SameTextThreshold {
		return nil, nil, false
	}

	reason := "known_policy_phrase"
	if !known {
		reason = fmt.Sprintf("same_text_x%d", occurrences)
	}
	logger.LegacyPrintf("service.gateway",
		"[RequestOwned429] short-circuit (reason=%s): Account=%d(%s) RequestID=%s Msg=%s",
		reason, account.ID, account.Name, resp.Header.Get("x-request-id"), truncateString(upstreamMsg, 300))

	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               "request_owned_429",
		Message:            upstreamMsg,
	})

	MarkResponseCommitted(c)
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		c.Header("Retry-After", ra)
	}
	if len(respBody) > 0 {
		c.Data(http.StatusTooManyRequests, "application/json", respBody)
	} else {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "rate_limit_error",
				"message": "Upstream rate limit exceeded, please retry later",
			},
		})
	}

	if upstreamMsg == "" {
		return nil, fmt.Errorf("upstream error: 429 request-owned (%s)", reason), true
	}
	return nil, fmt.Errorf("upstream error: 429 request-owned (%s) message=%s", reason, upstreamMsg), true
}

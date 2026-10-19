package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

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
// 429 OR a same-text-fanned 529: it writes the ORIGINAL upstream status + body
// through to the client (preserving the policy phrase for the next mirror hop),
// records ops telemetry, and returns handled=true so the caller skips account
// side effects and failover. Returns handled=false for everything else.
//
// 529 coverage (prod 2026-06-11 16:42–17:03 UTC Anthropic-wide overload event):
// the same-text breaker is status-generalized so an identical 529 "overloaded"
// text fanned across one request's failover chain stops poisoning the rest of a
// multi-account pool — failing over more during a provider-wide overload is
// pointless and cools every account. There is no request-owned PHRASE for 529
// (overload is provider state, not a request property), so 529 only ever takes
// the same-text path, never the known-phrase first-hop short-circuit.
//
// Call sites are the anthropic failover branches of Forward /
// forwardAnthropicAPIKeyPassthroughWithInput, after the error body has been
// read and BEFORE handleFailoverSideEffects / handleRetryExhaustedSideEffects.
func (s *GatewayService) tkHandleAnthropicRequestOwned429(c *gin.Context, account *Account, resp *http.Response, respBody []byte) (*ForwardResult, error, bool) {
	if c == nil || account == nil || resp == nil {
		return nil, nil, false
	}
	// 529 (overloaded) joins 429 here; both are in shouldFailoverUpstreamError so
	// both reach these call sites. http has no 529 constant — match the literal
	// the rest of the codebase uses.
	if (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != 529) || account.Platform != PlatformAnthropic {
		return nil, nil, false
	}
	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	normalized := strings.ToLower(upstreamMsg)

	known := tkIsAnthropicRequestOwned429Message(upstreamMsg, respBody)
	if !known {
		if tkSkipDownstreamNoAvailableAccountsPenalty(resp.StatusCode, upstreamMsg, respBody) ||
			tkSkipDownstreamFailoverExhaustedPenalty(resp.StatusCode, upstreamMsg, respBody) ||
			strings.Contains(normalized, "upstream rate limit exceeded") ||
			TkLooksLikeLegacyUpstreamLimitClientMessage(upstreamMsg) ||
			TkIsClientFacingBusy429Message(upstreamMsg) {
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
		"[RequestOwned429] short-circuit (status=%d reason=%s): Account=%d(%s) RequestID=%s Msg=%s",
		resp.StatusCode, reason, account.ID, account.Name, resp.Header.Get("x-request-id"), truncateString(upstreamMsg, 300))

	kind := "request_owned_429"
	if resp.StatusCode == 529 {
		kind = "request_owned_529"
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               kind,
		Message:            upstreamMsg,
	})

	MarkResponseCommitted(c)
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		c.Header("Retry-After", ra)
	}
	// Pass the ORIGINAL upstream status through (was hardcoded 429): a 529 must
	// reach the client as 529 so the prod mirror hop re-classifies it as overload,
	// not rate-limit. Empty-body fallback is keyed by status.
	if len(respBody) > 0 {
		c.Data(resp.StatusCode, "application/json", respBody)
	} else if resp.StatusCode == 529 {
		c.JSON(529, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "overloaded_error",
				"message": "Upstream service overloaded, please retry later",
			},
		})
	} else {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "rate_limit_error",
				"message": TkClientFacingBusy429Message,
			},
		})
	}

	if upstreamMsg == "" {
		return nil, fmt.Errorf("upstream error: %d request-owned (%s)", resp.StatusCode, reason), true
	}
	return nil, fmt.Errorf("upstream error: %d request-owned (%s) message=%s", resp.StatusCode, reason, upstreamMsg), true
}

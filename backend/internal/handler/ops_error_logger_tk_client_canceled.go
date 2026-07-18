package handler

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// tkUpstreamClientCanceled reports whether the upstream error captured on the
// request context is a CLIENT/caller disconnect (Go context.Canceled) rather than
// a provider/account-health failure or a server-side timeout.
//
// Why this exists (issue #625; prod + us2/us3/uk1 quad P0 2026-06-06T07:00-07:16Z):
// a single automated client issuing non-streaming claude-opus-4-6 with a client
// timeout shorter than opus latency cancels mid-flight. The outbound request fails
// with `context canceled` and NO upstream HTTP status, so classifyOpsErrorLog
// relabels it phase=upstream / error_owner=provider and it counts toward
// upstream_error_rate (the provider-health P0 capacity alert). Because prod relays
// the same request to edges via cc-* stub accounts, one client cancel propagates
// down the chain (client -> prod -> stub -> edge -> anthropic) and each node
// records its own 502 — so one canceling client floods upstream_error_rate on prod
// AND every edge it fans out to at once.
//
// Owning it to the client (phase=request) drops it out of upstream_error_rate,
// mirroring tkUpstreamClientInducedRejection. The fix lives at the per-node
// classification layer: every node checks ITS OWN inbound cancellation, so the
// same single predicate covers prod and all relayed edges uniformly.
//
// CAREFUL: context.Canceled (caller went away) is caller-fault and exempt; a
// server/upstream timeout surfaces as context.DeadlineExceeded ("deadline
// exceeded") and MUST stay provider-owned — it is genuine evidence of upstream
// slowness and SHOULD keep counting toward upstream_error_rate.
func tkUpstreamClientCanceled(c *gin.Context) bool {
	if c == nil {
		return false
	}
	// A definitive FINAL upstream HTTP status means we got an upstream verdict.
	// Do not scan backward to any older positive-status failover event here: a
	// request can receive 502s from earlier accounts and then be canceled by the
	// caller during the last attempt. In that sequence the terminal status-0
	// request_error is the final outcome, while the older 502 events remain useful
	// attempt-level provider evidence in upstream_errors.
	if tkOpsTerminalUpstreamStatusCode(c) != 0 {
		return false
	}
	// Primary signal: the inbound request context was canceled by the caller
	// disconnecting. net/http cancels Request.Context() when the client connection
	// goes away. A context error is either Canceled or DeadlineExceeded (never
	// both); we accept only Canceled so server-side timeouts stay provider-owned.
	if c.Request != nil {
		if reqErr := c.Request.Context().Err(); errors.Is(reqErr, context.Canceled) {
			return true
		}
	}
	// Fallback: the captured upstream transport error text is a context
	// cancellation. The forward-failure sites record the sanitized Go error
	// (sanitizeUpstreamErrorMessage preserves the "context canceled" substring), so
	// this catches the cancellation even when Request.Context() is unavailable.
	body, msg := tkOpsUpstreamErrorText(c)
	combined := strings.ToLower(strings.TrimSpace(msg + "\n" + body))
	if combined == "" {
		return false
	}
	// Must be the cancellation signature and NOT a deadline/timeout, which stays
	// provider-owned.
	if strings.Contains(combined, "deadline exceeded") ||
		strings.Contains(combined, "timeout") ||
		strings.Contains(combined, "timed out") {
		return false
	}
	return strings.Contains(combined, "context canceled") ||
		strings.Contains(combined, "context cancelled")
}

func tkOpsTerminalUpstreamStatusCode(c *gin.Context) int {
	if c == nil {
		return 0
	}
	// When an event chain exists, its last real event is the terminal outcome.
	// Status 0 is meaningful here: it records a transport failure such as caller
	// cancellation and must override a stale positive single-field status left by
	// an earlier failover attempt.
	if v, ok := c.Get(service.OpsUpstreamErrorsKey); ok {
		if events, ok := v.([]*service.OpsUpstreamErrorEvent); ok {
			for i := len(events) - 1; i >= 0; i-- {
				if events[i] != nil {
					return events[i].UpstreamStatusCode
				}
			}
		}
	}
	// Legacy paths may only populate the single-field status.
	if v, ok := c.Get(service.OpsUpstreamStatusCodeKey); ok {
		switch status := v.(type) {
		case int:
			if status > 0 {
				return status
			}
		case int64:
			if status > 0 {
				return int(status)
			}
		}
	}
	return 0
}

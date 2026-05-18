package service

import (
	"bytes"
	"context"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// TK: Anthropic thinking-block signature_error preempt.
//
// Problem: when an Anthropic OAuth account starts returning 400 + signature_error
// for thinking-block requests, the existing retry chain (gateway_service.go ~4621)
// recovers each request by stripping thinking blocks and retrying — BUT the first
// (original-body) upstream call is wasted on every request. On edge-us1 we observed
// 12 such bursts in 4 minutes on a single account, each one paying for an
// unnecessary upstream round trip.
//
// Strategy: keep a per-account sliding-window counter of signature_error
// occurrences. When the count crosses anthropicSigPreemptThreshold inside
// anthropicSigPreemptWindowSec, arm a "preempt cooldown" flag with TTL
// anthropicSigPreemptCooldownSec. While the flag is armed, requests routed to that
// account have their body pre-filtered with FilterThinkingBlocksForRetry BEFORE
// the first upstream attempt. The existing retry chain remains as a safety net.
//
// The retry chain (shouldRectifySignatureError) is the existing feature gate —
// when it is disabled (rectifier setting off), arm/apply both become no-ops via
// the call sites' surrounding conditionals.

const (
	anthropicSigPreemptThreshold   = 3
	anthropicSigPreemptWindowSec   = 120
	anthropicSigPreemptCooldownSec = 300
)

// SetAnthropicSigPreemptCache wires the Redis-backed preempt cache into
// GatewayService without changing the upstream constructor signature. Mirrors
// SetPricingAvailabilityService — called once during Wire DI; absence of the
// call leaves the feature disabled and the helpers below become no-ops.
func (s *GatewayService) SetAnthropicSigPreemptCache(c AnthropicSignaturePreemptCache) {
	if s != nil {
		s.tkAnthropicSigPreemptCache = c
	}
}

// HasAnthropicSigPreemptCache reports whether the preempt cache is wired. Used
// by DI smoke tests to prove the post-construction setter actually ran.
func (s *GatewayService) HasAnthropicSigPreemptCache() bool {
	return s != nil && s.tkAnthropicSigPreemptCache != nil
}

// applySigPreemptIfArmed pre-filters thinking blocks when the per-account
// preempt cooldown flag is currently armed. Returns the (possibly transformed)
// body. Safe to call with nil receiver, nil cache, or nil account — falls
// through and returns the original body unchanged.
//
// The returned body should replace the working body for the subsequent upstream
// attempt; the caller is responsible for calling setOpsUpstreamRequestBody if
// the body changed.
func (s *GatewayService) applySigPreemptIfArmed(ctx context.Context, c *gin.Context, account *Account, body []byte) []byte {
	if s == nil || s.tkAnthropicSigPreemptCache == nil || account == nil || len(body) == 0 {
		return body
	}
	armed, err := s.tkAnthropicSigPreemptCache.IsArmed(ctx, account.ID)
	if err != nil {
		// Fail-open: never block a request because the cache is sad.
		logger.LegacyPrintf("service.gateway", "[SigPreempt] IsArmed failed account=%d err=%v (fail-open)", account.ID, err)
		return body
	}
	if !armed {
		return body
	}
	filtered := FilterThinkingBlocksForRetry(body)
	transformed := !bytes.Equal(filtered, body)
	msg := "thinking_blocks_stripped"
	if !transformed {
		msg = "thinking_blocks_preempt_noop"
	}
	if c != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:    account.Platform,
			AccountID:   account.ID,
			AccountName: account.Name,
			Kind:        "sig_preempt_applied",
			Message:     msg,
		})
	}
	if transformed {
		logger.LegacyPrintf("service.gateway", "[SigPreempt] applied account=%d (cooldown active) — skipped first-shot signature_error", account.ID)
	}
	return filtered
}

// armSigPreemptOnError increments the per-account signature_error counter and,
// when it crosses the threshold, sets the preempt cooldown flag. Safe with nil
// receiver / nil cache / nil account / nil gin.Context. Idempotent across
// retries within the same request — caller should only invoke once per
// detected signature_error on the original (un-filtered) upstream response.
func (s *GatewayService) armSigPreemptOnError(ctx context.Context, c *gin.Context, account *Account) {
	if s == nil || s.tkAnthropicSigPreemptCache == nil || account == nil {
		return
	}
	count, armedNow, err := s.tkAnthropicSigPreemptCache.ArmIfThreshold(
		ctx,
		account.ID,
		anthropicSigPreemptThreshold,
		anthropicSigPreemptWindowSec,
		anthropicSigPreemptCooldownSec,
	)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "[SigPreempt] ArmIfThreshold failed account=%d err=%v (fail-open)", account.ID, err)
		return
	}
	if !armedNow {
		// Counter incremented but threshold not crossed (or flag already armed).
		// Skip ops event to avoid log noise; the per-request signature_error
		// event already records the underlying upstream 400.
		return
	}
	if c != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:    account.Platform,
			AccountID:   account.ID,
			AccountName: account.Name,
			Kind:        "sig_preempt_armed",
			Message:     "signature_error_threshold_crossed",
			Detail: "count=" + strconv.FormatInt(count, 10) +
				" threshold=" + strconv.Itoa(anthropicSigPreemptThreshold) +
				" cooldown_seconds=" + strconv.Itoa(anthropicSigPreemptCooldownSec),
		})
	}
	logger.LegacyPrintf("service.gateway", "[SigPreempt] armed account=%d count=%d threshold=%d cooldown=%ds", account.ID, count, anthropicSigPreemptThreshold, anthropicSigPreemptCooldownSec)
}

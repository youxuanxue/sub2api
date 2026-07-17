package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// TK: Claude-Code-only group fallback routing for the OpenAI-compatible paths
// (/v1/chat/completions, /v1/responses).
//
// Background: a group can be claude_code_only=true (CC-only): it only serves
// Claude Code clients via /v1/messages. A NON-CC request hitting a CC-only
// group on an OpenAI-compat path is otherwise hard-403'd. Operators configure
// a fallback_group_id on the CC-only group expecting non-CC traffic to fall
// back to that group instead of being rejected — this helper implements that
// intent. The companion upstream-shaped handlers keep a thin injection (see
// gateway_handler_chat_completions.go / gateway_handler_responses.go).
//
// Guards (all must hold to route to the fallback; otherwise the original 403
// stands — never silently route to a bad group):
//   - the CC-only group must declare a fallback_group_id (else keep the 403);
//   - the fallback group must resolve, be active, NOT be claude_code_only
//     (loop guard — re-entering the same 403), and be on the anthropic platform
//     (these OpenAI-compat paths only forward to the Anthropic upstream);
//   - billing eligibility for the fallback-bound key must pass.
//
// Single-hop only: the fallback is resolved exactly once, never chained.

// tkCCOnlyFallbackGroupValid is the pure validation predicate for a candidate
// CC-only fallback group. It is dependency-free so it can be unit-tested in
// isolation. nil is invalid.
func tkCCOnlyFallbackGroupValid(fallback *service.Group) bool {
	if fallback == nil {
		return false
	}
	// Loop guard: a CC-only fallback would re-trigger the same 403.
	if fallback.ClaudeCodeOnly {
		return false
	}
	// These OpenAI-compat paths only forward to the Anthropic upstream, so the
	// fallback must be servable there.
	if fallback.Platform != service.PlatformAnthropic {
		return false
	}
	return fallback.IsActive()
}

// tkResolveCCOnlyFallback is invoked at the CC-only guard of an OpenAI-compat
// handler when apiKey.Group.ClaudeCodeOnly is true. It returns:
//
//   - (fallbackAPIKey, false): a valid fallback was found and billing passed;
//     the caller must continue the request bound to fallbackAPIKey (the request
//     is served by the fallback group's accounts).
//   - (nil, true): the caller must return immediately — either the original 403
//     was written (no/invalid fallback), or a billing error was surfaced. The
//     helper has already written the response via writeForbidden / the billing
//     error writer.
//
// errWriter writes the canonical 403 for the calling endpoint (chat-completions
// vs responses error shapes differ). billingErrWriter writes a billing error
// (status/code/message) for the calling endpoint.
func (h *GatewayHandler) tkResolveCCOnlyFallback(
	c *gin.Context,
	apiKey *service.APIKey,
	reqLog *zap.Logger,
	writeForbidden func(),
	writeBillingError func(status int, code, message string),
) (fallbackAPIKey *service.APIKey, handled bool) {
	const ccOnlyForbiddenLog = "gateway.cc_only_fallback"

	if apiKey == nil || apiKey.Group == nil || apiKey.Group.FallbackGroupID == nil || *apiKey.Group.FallbackGroupID <= 0 {
		// No fallback configured — keep the existing 403.
		writeForbidden()
		return nil, true
	}

	fallbackGroupID := *apiKey.Group.FallbackGroupID
	fallbackGroup, err := h.gatewayService.ResolveGroupByID(c.Request.Context(), fallbackGroupID)
	if err != nil || fallbackGroup == nil {
		reqLog.Warn(ccOnlyForbiddenLog+".resolve_failed",
			zap.Int64("fallback_group_id", fallbackGroupID), zap.Error(err))
		writeForbidden()
		return nil, true
	}

	if !tkCCOnlyFallbackGroupValid(fallbackGroup) {
		reqLog.Warn(ccOnlyForbiddenLog+".invalid",
			zap.Int64("fallback_group_id", fallbackGroup.ID),
			zap.String("fallback_platform", fallbackGroup.Platform),
			zap.Bool("fallback_claude_code_only", fallbackGroup.ClaudeCodeOnly),
			zap.String("fallback_status", fallbackGroup.Status),
		)
		writeForbidden()
		return nil, true
	}

	clonedAPIKey := cloneAPIKeyWithGroup(apiKey, fallbackGroup)
	// Billing eligibility for the fallback-bound key. Subscription is nil here —
	// the fallback is a different group, so the original group's subscription
	// context does not apply (mirrors the PromptTooLongError fallback path).
	if err := h.billingCacheService.CheckBillingEligibility(
		c.Request.Context(),
		clonedAPIKey.User,
		clonedAPIKey,
		fallbackGroup,
		nil,
		service.PlatformFromAPIKey(clonedAPIKey),
	); err != nil {
		reqLog.Info(ccOnlyForbiddenLog+".billing_check_failed",
			zap.Int64("fallback_group_id", fallbackGroup.ID), zap.Error(err))
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		writeBillingError(status, code, message)
		return nil, true
	}

	reqLog.Info(ccOnlyForbiddenLog+".routed",
		zap.Any("from_group_id", apiKey.GroupID),
		zap.Int64("fallback_group_id", fallbackGroup.ID),
	)
	return clonedAPIKey, false
}

// tkCCOnlyForbiddenMessage is the canonical CC-only rejection message, shared by
// both OpenAI-compat handlers so the 403 text stays identical to upstream.
const tkCCOnlyForbiddenMessage = "This group is restricted to Claude Code clients (/v1/messages only)"

package handler

import (
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// TK: pre-flight balance HOLD injection for OpenAI-compatible routes.
//
// Reserves an upper-bound cost on the user balance BEFORE forwarding so that
// concurrent requests cannot overdraw it (the post-hoc deduct has no floor —
// see service/usage_billing_hold_tk.go for the invariant). Balance users only:
// subscription requests have no balance to overdraw and are skipped. The hold
// is released at request end via the returned closure (deferred by the caller).
//
// tkInsufficientBalanceForHoldMsg is the client-facing reason when the balance
// cannot cover the reservation.
const tkInsufficientBalanceForHoldMsg = "Insufficient account balance to reserve this request"

// tkHoldFallbackMaxOutputTokens is the conservative output-token upper bound
// used when a request omits max_tokens / max_completion_tokens /
// max_output_tokens. The hold must be an upper bound on actual cost; without a
// client-declared ceiling we assume a large output so the reservation cannot
// under-cover. (PR-follow-up: derive the exact per-model max-output from
// pricing metadata to tighten this.)
const tkHoldFallbackMaxOutputTokens = 200000

// tkParseMaxOutputTokens extracts the output-token ceiling from the request,
// falling back to a conservative bound when the client omits it. Field names
// per surface: max_tokens (chat, anthropic messages), max_completion_tokens
// (chat), max_output_tokens (responses) — max() of whatever is present.
func tkParseMaxOutputTokens(body []byte) int {
	m := int(gjson.GetBytes(body, "max_tokens").Int())
	if mc := int(gjson.GetBytes(body, "max_completion_tokens").Int()); mc > m {
		m = mc
	}
	if mo := int(gjson.GetBytes(body, "max_output_tokens").Int()); mo > m {
		m = mo
	}
	if m <= 0 {
		m = tkHoldFallbackMaxOutputTokens
	}
	return m
}

// tkApplyHold is the shared reserve-or-skip shell: skips subscription requests
// (no balance to overdraw), resolves the usage-billing request id, runs the
// route-specific reserve, and wraps the release closure. Returns:
//   - release != nil → a hold is held; caller MUST defer release().
//   - reject == true  → balance insufficient; caller returns its 403 and stops.
//   - both zero       → not gated (subscription / no hold capability / unpriced).
func (h *OpenAIGatewayHandler) tkApplyHold(c *gin.Context, apiKey *service.APIKey, reserve func(requestID string) (held bool, reject bool)) (release func(), reject bool) {
	if h == nil || h.gatewayService == nil || apiKey == nil || apiKey.User == nil {
		return nil, false
	}
	if subscription, _ := middleware2.GetSubscriptionFromContext(c); subscription != nil {
		return nil, false
	}
	requestID := service.TkResolveUsageBillingRequestID(c.Request.Context())
	held, rej := reserve(requestID)
	if rej {
		return nil, true
	}
	if held {
		ctx := c.Request.Context()
		return func() { h.gatewayService.TkReleaseHold(ctx, requestID) }, false
	}
	return nil, false
}

// tkApplyBalanceHold reserves a pre-flight token hold for the output-bearing
// token routes (chat / responses / messages).
//
// promptTokens upper bound = len(body): a BPE token spans ≥ 1 body byte, so the
// byte count is a safe (loose) upper bound on input tokens without a tokenizer.
func (h *OpenAIGatewayHandler) tkApplyBalanceHold(c *gin.Context, apiKey *service.APIKey, reqModel string, body []byte) (release func(), reject bool) {
	return h.tkApplyTokenHold(c, apiKey, reqModel, body, tkParseMaxOutputTokens(body))
}

// tkApplyBalanceHoldNoOutput is tkApplyBalanceHold for routes that produce no
// output tokens (embeddings): the output side of the estimate is zero instead
// of the fallback ceiling, so an embeddings call is not blocked by a hold three
// orders of magnitude above its real cost.
func (h *OpenAIGatewayHandler) tkApplyBalanceHoldNoOutput(c *gin.Context, apiKey *service.APIKey, reqModel string, body []byte) (release func(), reject bool) {
	return h.tkApplyTokenHold(c, apiKey, reqModel, body, 0)
}

func (h *OpenAIGatewayHandler) tkApplyTokenHold(c *gin.Context, apiKey *service.APIKey, reqModel string, body []byte, maxOutputTokens int) (release func(), reject bool) {
	return h.tkApplyHold(c, apiKey, func(requestID string) (bool, bool) {
		return h.gatewayService.TkReserveTokenHold(
			c.Request.Context(),
			requestID,
			reqModel,
			gjson.GetBytes(body, "service_tier").String(),
			apiKey.User,
			apiKey,
			len(body),
			maxOutputTokens,
		)
	})
}

// tkApplyImageHold reserves a pre-flight hold for an image-generation request
// (n = requested image count; actual delivers ≤ n).
func (h *OpenAIGatewayHandler) tkApplyImageHold(c *gin.Context, apiKey *service.APIKey, reqModel string, n int) (release func(), reject bool) {
	return h.tkApplyHold(c, apiKey, func(requestID string) (bool, bool) {
		return h.gatewayService.TkReserveImageHold(c.Request.Context(), requestID, reqModel, apiKey.User, apiKey, n)
	})
}

// tkApplyVideoHold reserves a pre-flight hold for an async video submit
// (seconds = the same request-derived duration the submit path bills).
func (h *OpenAIGatewayHandler) tkApplyVideoHold(c *gin.Context, apiKey *service.APIKey, reqModel string, seconds int64) (release func(), reject bool) {
	return h.tkApplyHold(c, apiKey, func(requestID string) (bool, bool) {
		return h.gatewayService.TkReserveVideoHold(c.Request.Context(), requestID, reqModel, apiKey.User, apiKey, seconds)
	})
}

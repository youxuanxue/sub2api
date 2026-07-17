package handler

import (
	"context"
	"strconv"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// TK: pre-flight balance HOLD injection for OpenAI-compatible routes.
//
// Reserves a pre-flight amount on the user balance BEFORE forwarding so
// concurrent requests cannot freely overdraw it (the post-hoc deduct has no
// floor — see service/usage_billing_hold_tk.go). Balance users only:
// subscription requests have no balance to overdraw and are skipped.
//
// Hold lifecycle (the release-before-settle rule): the reservation must outlive
// the handler whenever a usage-record task was submitted, because the actual
// balance deduction runs in that async task — refunding at handler return would
// re-expose the funds to concurrent admission until the bill lands. So the
// handler defers ReleaseUnlessSettling() and, at each usage-task submit site,
// synchronously calls HandOffToSettlement(); settlement then consumes the hold
// in the same transaction as the deduction (usage_billing_repo_tk_hold.go).
// Crash after hand-off is repaid by the hold reconciler.
//
// tkInsufficientBalanceForHoldMsg is the client-facing reason when the balance
// cannot cover the reservation (rejected 403, matching billingErrorDetails'
// insufficient-balance mapping).
const tkInsufficientBalanceForHoldMsg = "Insufficient account balance to reserve this request"

// tkHoldDefaultOutputReserveTokens is the UX-friendly output-token reserve used
// when a request omits max_tokens / max_completion_tokens / max_output_tokens.
// Explicit client ceilings are still honored as hard reserve inputs; only the
// omitted-ceiling path uses this lower estimate so a short-lived auth/balance
// cache snapshot does not reject hundreds of ordinary requests.
const tkHoldDefaultOutputReserveTokens = 256

// tkParseMaxOutputTokens extracts the output-token ceiling from the request,
// falling back to a low default reserve when the client omits it. Field names
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
		m = tkHoldDefaultOutputReserveTokens
	}
	return m
}

// tkHoldHandle owns one reservation's release. Nil-safe: a nil handle (request
// not gated) makes every method a no-op, so call sites need no nil checks. Not
// goroutine-safe — HandOffToSettlement must be called from the handler
// goroutine BEFORE the deferred ReleaseUnlessSettling can run (i.e. before the
// handler returns), which every submit site satisfies by construction.
type tkHoldHandle struct {
	h         *OpenAIGatewayHandler
	ctx       context.Context
	requestID string
	settling  bool
}

// ReleaseUnlessSettling refunds the hold at handler return — the error-path
// cleanup. Once handed off to settlement it is a no-op: the settlement
// transaction (or, after a crash, the reconciler) owns the refund.
func (hh *tkHoldHandle) ReleaseUnlessSettling() {
	if hh == nil || hh.settling {
		return
	}
	hh.h.gatewayService.TkReleaseHold(hh.ctx, hh.requestID)
}

// HandOffToSettlement transfers refund ownership to the usage-record task and
// returns the hold key to stamp into OpenAIRecordUsageInput.TkHoldRequestID.
// MUST be called synchronously at the submit site (not inside the async task
// closure), or the deferred release races the hand-off.
func (hh *tkHoldHandle) HandOffToSettlement() string {
	if hh == nil {
		return ""
	}
	hh.settling = true
	return hh.requestID
}

// tkApplyHold is the shared reserve-or-skip shell: skips subscription requests
// (no balance to overdraw), resolves the usage-billing request id (plus an
// optional suffix for sub-request holds, e.g. WS turns), runs the
// route-specific reserve, and wraps the handle. Returns:
//   - hold != nil → reserved; caller MUST defer hold.ReleaseUnlessSettling().
//   - reject == true → balance insufficient; caller returns its 403 and stops.
//   - both zero → not gated (subscription / no hold capability / unpriced).
func (h *OpenAIGatewayHandler) tkApplyHold(c *gin.Context, apiKey *service.APIKey, requestIDSuffix string, reserve func(requestID string) (held bool, reject bool)) (hold *tkHoldHandle, reject bool) {
	if h == nil || h.gatewayService == nil || apiKey == nil || apiKey.User == nil {
		return nil, false
	}
	if subscription, _ := middleware2.GetSubscriptionFromContext(c); subscription != nil {
		return nil, false
	}
	requestID := service.TkResolveUsageBillingRequestID(c.Request.Context())
	if requestID == "" {
		return nil, false
	}
	requestID += requestIDSuffix
	held, rej := reserve(requestID)
	if rej {
		return nil, true
	}
	if held {
		return &tkHoldHandle{h: h, ctx: c.Request.Context(), requestID: requestID}, false
	}
	return nil, false
}

// tkApplyBalanceHold reserves a pre-flight token hold for the output-bearing
// token routes (chat / responses / messages).
//
// promptTokens upper bound = len(body): a BPE token spans ≥ 1 body byte, so the
// byte count is a safe (loose) upper bound on input tokens without a tokenizer.
func (h *OpenAIGatewayHandler) tkApplyBalanceHold(c *gin.Context, apiKey *service.APIKey, reqModel string, body []byte) (hold *tkHoldHandle, reject bool) {
	return h.tkApplyTokenHold(c, apiKey, reqModel, body, tkParseMaxOutputTokens(body), "")
}

// tkApplyBalanceHoldNoOutput is tkApplyBalanceHold for routes that produce no
// output tokens (embeddings): the output side of the estimate is zero instead
// of the default reserve, so an embeddings call is not blocked by a hold three
// orders of magnitude above its real cost.
func (h *OpenAIGatewayHandler) tkApplyBalanceHoldNoOutput(c *gin.Context, apiKey *service.APIKey, reqModel string, body []byte) (hold *tkHoldHandle, reject bool) {
	return h.tkApplyTokenHold(c, apiKey, reqModel, body, 0, "")
}

// tkApplyWSTurnHold reserves a per-turn token hold on the Responses WebSocket
// ingress. Each turn is billed separately (per-turn RecordUsage with an
// upstream-derived request id), so each turn gets its own hold, keyed by the
// connection's billing request id + the turn ordinal.
func (h *OpenAIGatewayHandler) tkApplyWSTurnHold(c *gin.Context, apiKey *service.APIKey, reqModel string, payload []byte, turn int) (hold *tkHoldHandle, reject bool) {
	return h.tkApplyTokenHold(c, apiKey, reqModel, payload, tkParseMaxOutputTokens(payload), ":wsturn:"+strconv.Itoa(turn))
}

func (h *OpenAIGatewayHandler) tkApplyTokenHold(c *gin.Context, apiKey *service.APIKey, reqModel string, body []byte, maxOutputTokens int, requestIDSuffix string) (hold *tkHoldHandle, reject bool) {
	return h.tkApplyHold(c, apiKey, requestIDSuffix, func(requestID string) (bool, bool) {
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
func (h *OpenAIGatewayHandler) tkApplyImageHold(c *gin.Context, apiKey *service.APIKey, reqModel string, n int) (hold *tkHoldHandle, reject bool) {
	return h.tkApplyHold(c, apiKey, "", func(requestID string) (bool, bool) {
		return h.gatewayService.TkReserveImageHold(c.Request.Context(), requestID, reqModel, apiKey.User, apiKey, n)
	})
}

// tkApplyVideoHold reserves a pre-flight hold for an async video submit
// (seconds = the same request-derived duration the submit path bills).
func (h *OpenAIGatewayHandler) tkApplyVideoHold(c *gin.Context, apiKey *service.APIKey, reqModel string, seconds int64) (hold *tkHoldHandle, reject bool) {
	return h.tkApplyHold(c, apiKey, "", func(requestID string) (bool, bool) {
		return h.gatewayService.TkReserveVideoHold(c.Request.Context(), requestID, reqModel, apiKey.User, apiKey, seconds)
	})
}

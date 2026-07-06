package handler

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// tkNoAvailableAccountsRetryAfterSeconds is the Retry-After hint (seconds) sent
// with the empty-pool fast-fail. Short enough that a transient pool gap recovers
// quickly; long enough that clients back off instead of immediately retrying.
const tkNoAvailableAccountsRetryAfterSeconds = "5"

// tkNoAvailableAccounts is the gateway response status when a scheduling pool has
// no schedulable account ("No available accounts"). It deliberately returns 429
// (Too Many Requests) + Retry-After instead of 503, setting the Retry-After header
// as a side effect before the caller writes the body. Pass it in the status
// position of any streaming-aware error writer, e.g.:
//
//	h.handleStreamingAwareError(c, tkNoAvailableAccounts(c), "api_error", "No available accounts", streamStarted)
//
// WHY 429 not 503 — prod flood incident 2026-06 (deepseek-v4-flash empty newapi
// pool, single IP 58.213.121.60 peaking 1540 RPM):
//   - Stage0 runs a single app upstream behind Caddy. Caddy passive health lists
//     503 in unhealthy_status with max_fails=1, so one empty-pool 503 marks the
//     sole upstream "down"; with no failover target every subsequent request
//     (including the dashboard at https://api.tokenkey.dev/) stalls in
//     lb_try_duration. A flood of empty-pool 503s thus synthesizes a fake
//     backend outage and melts the node. 429 is NOT in unhealthy_status, so it
//     never trips passive health.
//   - 503 makes SDK clients retry aggressively (retry storm); 429 + Retry-After
//     makes them back off.
//
// Go evaluates call arguments left-to-right before the call, so the header is set
// before the error writer runs. For account-select failures the response has not
// started, so the header lands on the wire.
func tkNoAvailableAccounts(c *gin.Context) int {
	c.Header("Retry-After", tkNoAvailableAccountsRetryAfterSeconds)
	return http.StatusTooManyRequests
}

// tkSelectFailureStatusMessage maps a FIRST-attempt account-selection error
// (len(failedAccountIDs)==0, no upstream attempt was made yet) to the
// client-facing (status, errType, message) triple. errType is the JSON error
// envelope "type" the caller should write (OpenAI-compat: "invalid_request_error"
// for 4xx client faults, "api_error" otherwise).
//
// Unsupported model — service.ErrUnsupportedModel: the scheduler determined NO
// account in the pool serves this model NAME (e.g. a legacy/typo'd id like
// "deepseek-chat" or "qwen-max" when the matched newapi pool only maps
// "deepseek-v4-pro" / "qwen3.7-*"). That is a CLIENT error, not capacity, so it
// gets 400 invalid_request_error (parity with the anthropic path's
// tkWriteUnsupportedModelIfApplicable). Surfacing it as 400 keeps it client-owned
// (phase=request, out of upstream_error_rate) instead of an empty-pool 429 that
// reads as a capacity signal and fires ops alerts (prod 2026-06-13: a client
// hammering unservable deepseek/qwen names produced 8× empty-pool 429). Checked
// FIRST: ErrUnsupportedModel's message deliberately omits "no available accounts"
// so isOpsNoAvailableAccountError does not also match it.
//
// Empty pool — the "no available accounts" error family, classified by
// isOpsNoAvailableAccountError, the exact predicate ops logging already uses in
// markOpsRoutingCapacityLimitedIfNoAvailable — gets the #575 fast-fail
// semantics: 429 + Retry-After via tkNoAvailableAccounts (see that helper's
// comment for why 503 melts the node behind Caddy passive health). Any other
// scheduler failure (DB outage, context errors, ...) keeps 503: those are real
// service faults, not capacity backoff hints.
//
// This converged the openai/newapi compat handlers with the anthropic path,
// which adopted the 429 semantics in #575; before this helper the two branches
// of the same handler disagreed (selection==nil → 429, select error → 503).
func tkSelectFailureStatusMessage(c *gin.Context, err error, reqModel string) (int, string, string) {
	if errors.Is(err, service.ErrDeprecatedAnthropicModel) {
		markOpsClientRequestRejected(c)
		if _, replacement, ok := service.TkLookupDeprecatedAnthropicModel(reqModel); ok {
			return http.StatusBadRequest, service.TkDeprecatedAnthropicErrorType,
				service.TkBuildDeprecatedAnthropicMessage(reqModel, replacement)
		}
		return http.StatusBadRequest, service.TkDeprecatedAnthropicErrorType, "Model is retired or scheduled for sunset by Anthropic"
	}
	if errors.Is(err, service.ErrUnsupportedModel) {
		// Own this to the client in ops regardless of the response envelope
		// (/responses carries the type in `code`, not `type`).
		markOpsClientRequestRejected(c)
		return http.StatusBadRequest, service.TkUnsupportedModelErrType, service.TkUnsupportedModelMessage(reqModel)
	}
	if isOpsNoAvailableAccountError(err) {
		return tkNoAvailableAccounts(c), "api_error", "No available accounts: " + err.Error()
	}
	return http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable"
}

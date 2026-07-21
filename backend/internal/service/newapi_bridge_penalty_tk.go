package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// TK: newapi fifth-platform bridge → account penalty wiring.
//
// Why this file exists (prod 2026-06-11, account 39 "ds-官" / DeepSeek):
// every bridge.Dispatch* error path used to call only tkWrapBridgeRelayError,
// which records the upstream verdict for OPS LOGGING and wraps the error —
// it never reaches RateLimitService.HandleUpstreamError. So an upstream 402
// "Insufficient Balance" left the account schedulable=true with no cooldown,
// no disable and no Feishu alert; the gateway hammered the exhausted upstream
// ~6946 times in 2h. The pre-existing "case 402 → handleAuthError +
// shouldDisable" in ratelimit_service.go was simply unreachable for newapi
// accounts because nothing on the bridge path called into it.
//
// tkHandleBridgeUpstreamPenalty closes that gap. Design constraints:
//
//   - Status allowlist {401, 402, 429} ONLY. 400 and 404 are deliberately
//     excluded: they are client-induced (bad request / unknown model name) and
//     penalizing them lets any caller drain the pool (the #617 lesson where
//     client 400s were mistaken for upstream faults and cooled healthy
//     accounts). The allowlist also automatically excludes the synthetic
//     bridge errors (errBridgeMissingCredential / errBridgeVideoUnsupportedChannel
//     are 400) — and as a second layer, the wrapper below is only invoked at
//     sites wrapping a REAL bridge.Dispatch* upstream error, never for the
//     synthetic early-returns.
//   - headers are not available from *newapitypes.NewAPIError, so we pass nil.
//     That is safe: http.Header.Get on a nil map returns "" (no reset-time
//     headers found), and for PlatformNewAPI handle429 then falls back to the
//     body parser and finally apply429FallbackRateLimit (default short
//     cooldown) — exactly the conservative behaviour we want.
//   - The account-state write must survive client cancellation: we reuse
//     openAIAccountStateContext (context.WithoutCancel + short timeout), the
//     same pattern as handleOpenAIAccountUpstreamError. A canceled request
//     must not abort the SetError/SetRateLimited write (the #628 class).
func tkHandleBridgeUpstreamPenalty(ctx context.Context, rls *RateLimitService, account *Account, apiErr *newapitypes.NewAPIError) {
	if rls == nil || account == nil || apiErr == nil {
		return
	}
	// TK (prod 2026-06-12, account 60 "Qwen" / DashScope arrears): an upstream
	// account-standing / arrears 400 ("Arrearage") is an ACCOUNT-level failure
	// disguised as a client 400. It must be caught BEFORE the status allowlist
	// below (which deliberately excludes 400 to avoid the #617 client-400
	// pool-drain). This narrow exception disables the account + fires an immediate
	// P0 Feishu card. See newapi_bridge_arrears_tk.go.
	if tkHandleBridgeArrearsPenalty(ctx, rls, account, apiErr) {
		return
	}
	if !tkBridgePenaltyStatusEligible(apiErr.StatusCode) {
		return
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	shouldDisable := rls.HandleUpstreamError(stateCtx, account, apiErr.StatusCode, nil, tkBridgeUpstreamErrorBody(apiErr))
	slog.Warn("newapi_bridge_upstream_penalty",
		"account_id", account.ID,
		"platform", account.Platform,
		"channel_type", account.ChannelType,
		"status_code", apiErr.StatusCode,
		"should_disable", shouldDisable,
	)
}

// tkBridgePenaltyStatusEligible is the explicit allowlist of upstream statuses
// that may penalize the account from the bridge path. Keep this narrow: adding
// 403/5xx here means a transient upstream WAF blip or provider outage could
// permanently disable accounts via handle403/custom-code paths — widen only
// with production evidence.
func tkBridgePenaltyStatusEligible(statusCode int) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests:
		return true
	}
	return false
}

// tkBridgeUpstreamErrorBody synthesizes an OpenAI-style error envelope from the
// bridge relay error so RateLimitService body parsers (extractUpstreamErrorMessage,
// parseOpenAIRateLimitResetTime, …) see the upstream message/code. NewAPIError
// does not retain the raw upstream response body; ToOpenAIError() is the closest
// faithful projection (it preserves the upstream message and structured code).
func tkBridgeUpstreamErrorBody(apiErr *newapitypes.NewAPIError) []byte {
	if apiErr == nil {
		return nil
	}
	body, err := json.Marshal(map[string]any{"error": apiErr.ToOpenAIError()})
	if err != nil {
		return nil
	}
	return body
}

// tkBridgeUpstreamOpenAIError returns the raw upstream OpenAI-style envelope
// stored on RelayError before ToOpenAIError() applies MaskSensitiveInfo. Alert
// detail lines must use this: masking turns help.aliyun.com URLs into
// https://***.com/***/***/***, which Feishu lark_md then renders as https://.com///.
func tkBridgeUpstreamOpenAIError(apiErr *newapitypes.NewAPIError) (newapitypes.OpenAIError, bool) {
	if apiErr == nil {
		return newapitypes.OpenAIError{}, false
	}
	oai, ok := apiErr.RelayError.(newapitypes.OpenAIError)
	return oai, ok
}

// tkWrapBridgeRelayErrorWithPenalty is the OpenAIGatewayService dispatch-site
// chokepoint: apply the account penalty for real upstream bridge errors, then
// return UpstreamFailoverError for account-level faults (401/402/429 + arrears)
// or NewAPIRelayError for client/outage errors. Use at every dispatch site that
// wraps a REAL bridge upstream error and has the selected account in hand
// (NOT for synthetic missing-credential / unsupported-channel errors, and NOT
// for the account-agnostic video fetch path).
func (s *OpenAIGatewayService) tkWrapBridgeRelayErrorWithPenalty(ctx context.Context, c *gin.Context, account *Account, apiErr *newapitypes.NewAPIError) error {
	var rls *RateLimitService
	if s != nil {
		rls = s.rateLimitService
	}
	return bridgeWrapRelayErrorAfterPenalty(ctx, rls, c, account, apiErr)
}

// tkWrapBridgeRelayErrorWithPenalty is the GatewayService sibling (the Anthropic
// gateway's chat-completions/responses bridge boundary in gateway_bridge_dispatch.go).
func (s *GatewayService) tkWrapBridgeRelayErrorWithPenalty(ctx context.Context, c *gin.Context, account *Account, apiErr *newapitypes.NewAPIError) error {
	var rls *RateLimitService
	if s != nil {
		rls = s.rateLimitService
	}
	return bridgeWrapRelayErrorAfterPenalty(ctx, rls, c, account, apiErr)
}

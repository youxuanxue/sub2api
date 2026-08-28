package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

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
	// TK: upstream account-standing / arrears (DashScope 400 Arrearage, or
	// Moonshot 429 + insufficient-balance suspend) is an ACCOUNT-level failure.
	// It must be caught BEFORE the status allowlist below (400 is excluded to
	// avoid the #617 client-400 pool-drain; 429 otherwise becomes a 5s cooldown).
	// This narrow exception disables the account + fires an immediate P0 Feishu
	// card. See newapi_bridge_arrears_tk.go.
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
// does not retain the raw upstream response body; prefer the raw RelayError
// OpenAIError (before ToOpenAIError MaskSensitiveInfo) so ops/alert paths keep
// public DashScope help URLs intact.
func tkBridgeUpstreamErrorBody(apiErr *newapitypes.NewAPIError) []byte {
	if apiErr == nil {
		return nil
	}
	var envelope any
	if oai, ok := tkBridgeUpstreamOpenAIError(apiErr); ok {
		envelope = oai
	} else {
		envelope = apiErr.ToOpenAIError()
	}
	body, err := json.Marshal(map[string]any{"error": envelope})
	if err != nil {
		return nil
	}
	return body
}

// tkBridgeUpstreamRelayMessage returns the upstream message for ops_error_logs /
// alert root-cause lines. ToOpenAIError() and MaskSensitiveInfo turn public
// help.aliyun.com URLs into https://***.com/***/***/***, which Feishu lark_md
// then renders as https://.com/// — use the raw RelayError envelope instead.
func tkBridgeUpstreamRelayMessage(apiErr *newapitypes.NewAPIError) string {
	if apiErr == nil {
		return ""
	}
	code := strings.TrimSpace(string(apiErr.GetErrorCode()))
	msg := ""
	if oai, ok := tkBridgeUpstreamOpenAIError(apiErr); ok {
		msg = strings.TrimSpace(oai.Message)
	}
	if msg == "" {
		msg = strings.TrimSpace(apiErr.Error())
	}
	if code != "" && msg != "" {
		return code + ": " + msg
	}
	return msg
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

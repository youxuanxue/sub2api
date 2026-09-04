package service

import (
	"context"
	"log/slog"
	"net/http"
)

// tkTryHandleUpstreamErrorPrelude collapses the TokenKey-only early-return chain
// at the top of HandleUpstreamError into one companion call site.
//
// Order is load-bearing and must stay identical to the prior inline chain:
// foreign-credential official-OpenAI reject → OpenAI-compat downstream capacity
// → CloudWise model-balance 402 → CloudWise provider 424 → standing billing.
//
// When handled is false the caller continues the upstream-shaped pool-mode /
// custom-error / temp-unschedulable path. cloudwiseModelBalance402 is recomputed
// by the caller for those carve-outs (cheap predicate).
func (s *RateLimitService) tkTryHandleUpstreamErrorPrelude(
	ctx context.Context,
	account *Account,
	statusCode int,
	_ http.Header,
	responseBody []byte,
	requestedModel string,
) (handled, shouldDisable bool) {
	if s == nil || account == nil {
		return true, false
	}

	if IsForeignCredentialOfficialOpenAIReject(account, statusCode, responseBody) {
		slog.Error("foreign_credential_official_openai_reject_skip_penalty",
			"account_id", account.ID,
			"platform", account.Platform,
			"channel_type", account.ChannelType,
			"status_code", statusCode,
			"expected_base_url", nativeOpenAIBaseURLForAccount(account))
		return true, false
	}

	if s.handleOpenAICompatDownstreamCapacityPenalty(ctx, account, statusCode, responseBody) {
		return true, true
	}

	// CloudWise model pools are independent. Handle their model-balance 402
	// before pool-mode and custom-error-code early exits so every CloudWise
	// account shape persists the same exact-model five-hour cooldown. If the
	// model scope cannot be persisted, bypass those early exits and retain the
	// conservative account-level 402 fallback.
	cloudwiseModelBalance402 := tkIsCloudwiseModelBalance402Response(account, statusCode, responseBody)
	if cloudwiseModelBalance402 && s.tkTryCloudwiseModelBalanceCooldown(ctx, account, statusCode, responseBody, requestedModel) {
		return true, true
	}
	if s.tkTryCloudwiseProvider424Cooldown(ctx, account, statusCode, responseBody, requestedModel) {
		return true, true
	}

	// Account-standing prepaid/balance exhaustion (tokensea 用户额度不足, 402
	// Insufficient Balance, Anthropic credit balance, …) is the same SSOT as
	// newapi_arrears: SetError + immediate Feishu P0. Must beat pool-mode skip
	// and tryTempUnschedulable so a generic 403 rule cannot flap a dead prepaid
	// account (prod 2026-08-30 tokensea-cc #93).
	if s.tkTryHandleStandingBilling(ctx, account, statusCode, responseBody) {
		return true, true
	}

	return false, false
}

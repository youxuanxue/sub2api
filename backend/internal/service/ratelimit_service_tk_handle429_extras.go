package service

import (
	"context"
	"log/slog"
	"net/http"
)

// tkTryHandle429Extras collapses the TokenKey-only early-return chain at the
// top of HandleUpstreamError case 429 (request-owned / downstream capacity /
// non-authoritative envelope skips) into one companion call site.
//
// When handled is false the caller continues into handle429 + stub-health fuse.
func (s *RateLimitService) tkTryHandle429Extras(
	ctx context.Context,
	account *Account,
	statusCode int,
	headers http.Header,
	upstreamMsg string,
	responseBody []byte,
) (handled, shouldDisable bool) {
	if s == nil || account == nil {
		return true, false
	}
	if account.Platform == PlatformAnthropic && tkIsAnthropicRequestOwned429Message(upstreamMsg, responseBody) {
		slog.Info("anthropic_request_owned_429_skip_penalty",
			"account_id", account.ID,
			"status_code", statusCode)
		return true, true
	}
	if account.Platform == PlatformAnthropic && tkSkipDownstreamNoAvailableAccountsPenalty(statusCode, upstreamMsg, responseBody) {
		slog.Info("anthropic_downstream_no_available_accounts_skip_penalty",
			"account_id", account.ID,
			"status_code", statusCode)
		s.recordAnthropicStubSaturation(ctx, account.ID, statusCode, "no_available_accounts")
		return true, true
	}
	if account.Platform == PlatformAnthropic && tkSkipDownstreamFailoverExhaustedPenalty(statusCode, upstreamMsg, responseBody) {
		slog.Info("anthropic_downstream_failover_exhausted_skip_penalty",
			"account_id", account.ID,
			"status_code", statusCode)
		s.recordAnthropicStubSaturation(ctx, account.ID, statusCode, "all_available_accounts_exhausted")
		return true, true
	}
	if tkSkipDownstreamKiroServiceUnavailablePenalty(account, statusCode, upstreamMsg, responseBody) {
		slog.Info("anthropic_downstream_kiro_service_unavailable_skip_penalty",
			"account_id", account.ID,
			"status_code", statusCode)
		s.recordAnthropicStubSaturation(ctx, account.ID, statusCode, "kiro_service_unavailable")
		return true, true
	}
	if account.Platform == PlatformAnthropic && tkIsAnthropicNonAuthoritative429(headers, responseBody) {
		tkLogAnthropicNonAuthoritative429Skip(account, statusCode)
		s.recordAnthropicStubSaturation(ctx, account.ID, statusCode, "non_authoritative_429")
		return true, true
	}
	return false, false
}

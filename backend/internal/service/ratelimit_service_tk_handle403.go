package service

import (
	"context"
	"net/http"
)

// tkHandleNonOpenAI403 owns the non-OpenAI/CN 403 chain that runs after
// handle403 returns from Antigravity / OpenAI / CN providers.
func (s *RateLimitService) tkHandleNonOpenAI403(
	ctx context.Context,
	account *Account,
	upstreamMsg string,
	responseBody []byte,
) (shouldDisable bool) {
	if s == nil || account == nil {
		return false
	}
	if s.tkMaybeRefreshKiroInvalidBearer403(ctx, account, upstreamMsg, responseBody) {
		return true
	}
	if account.Platform == PlatformAnthropic {

		if s.tkTryDisableAnthropicOrgBan403(ctx, account, upstreamMsg, responseBody) {
			return true
		}
		if s.tkTryDisableAnthropicTLSFingerprint403(ctx, account, upstreamMsg, responseBody) {
			return true
		}
		if s.tkTryEscalatePersistentBodyless403(ctx, account, upstreamMsg, responseBody) {
			return true
		}
		if tkIsAnthropicAccountAuthFatal403(upstreamMsg, responseBody) {
			msg := buildForbiddenErrorMessage(
				"Access forbidden (403):",
				upstreamMsg,
				responseBody,
				"account may be suspended or lack permissions",
			)
			s.handleAuthError(ctx, account, msg)
			return true
		}
		s.recordAnthropicStubSaturation(ctx, account.ID, http.StatusForbidden, "permission_failover")
		return true
	}
	// 非 Antigravity 平台：保持原有行为
	msg := buildForbiddenErrorMessage(
		"Access forbidden (403):",
		upstreamMsg,
		responseBody,
		"account may be suspended or lack permissions",
	)
	s.handleAuthError(ctx, account, msg)
	return true
}

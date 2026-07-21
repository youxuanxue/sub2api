package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

// tkIsOpenAIEdgeMirrorStub reports whether account is a prod OpenAI apikey stub
// that forwards to an internal edge gateway (credentials.base_url
// https://api-<edge>.tokenkey.dev). Downstream "No available accounts" on these
// stubs is an edge-pool capacity signal, not OpenAI account health.
func tkIsOpenAIEdgeMirrorStub(account *Account) bool {
	return account != nil && account.Platform == PlatformOpenAI && isEdgeMirrorStub(account, edgeIDPattern)
}

// tkIsOpenAICompatEdgeMirrorStub reports whether account is an OpenAI-compatible
// prod apikey relay stub backed by an internal edge gateway.
func tkIsOpenAICompatEdgeMirrorStub(account *Account) bool {
	if account == nil || account.Type != AccountTypeAPIKey || !isEdgeMirrorStub(account, edgeIDPattern) {
		return false
	}
	if account.Platform == PlatformOpenAI || account.Platform == PlatformGrok {
		return account.Platform == PlatformGrok || tkIsOpenAIEdgeMirrorStub(account)
	}
	return false
}

func tkIsDownstreamRateLimitEnvelope(statusCode int, upstreamMsg string, responseBody []byte) bool {
	if statusCode != http.StatusTooManyRequests {
		return false
	}
	hay := strings.ToLower(strings.TrimSpace(upstreamMsg) + "\n" + string(responseBody))
	return strings.Contains(hay, "upstream rate limit exceeded")
}

func tkOpenAICompatDownstreamCapacityReason(statusCode int, upstreamMsg string, responseBody []byte) string {
	switch {
	case tkSkipDownstreamNoAvailableAccountsPenalty(statusCode, upstreamMsg, responseBody):
		return "no_available_accounts"
	case tkSkipDownstreamFailoverExhaustedPenalty(statusCode, upstreamMsg, responseBody):
		// Historical metrics key; keep it stable across the client-message change.
		return "all_available_accounts_exhausted"
	case tkIsDownstreamRateLimitEnvelope(statusCode, upstreamMsg, responseBody):
		return "upstream_rate_limit_exhausted"
	default:
		return "downstream_capacity"
	}
}

// tkSkipOpenAIDownstreamCapacityPenalty is true when an OpenAI-compatible
// edge-mirror stub received TokenKey's own downstream pool-exhaustion envelope.
// Fail over without handle429 cooldown / runtime block and feed the bounded
// saturation preference.
func tkSkipOpenAIDownstreamCapacityPenalty(account *Account, statusCode int, upstreamMsg string, responseBody []byte) bool {
	if !tkIsOpenAICompatEdgeMirrorStub(account) {
		return false
	}
	if tkSkipDownstreamNoAvailableAccountsPenalty(statusCode, upstreamMsg, responseBody) {
		return true
	}
	if tkSkipDownstreamFailoverExhaustedPenalty(statusCode, upstreamMsg, responseBody) {
		return true
	}
	return tkIsDownstreamRateLimitEnvelope(statusCode, upstreamMsg, responseBody)
}

func tkOpenAICompatRetryableOnSameAccount(account *Account, statusCode int, upstreamMsg string, responseBody []byte, includeTransient bool) bool {
	if account == nil || !account.IsPoolMode() {
		return false
	}
	if tkSkipOpenAIDownstreamCapacityPenalty(account, statusCode, upstreamMsg, responseBody) {
		return false
	}
	if account.IsPoolModeRetryableStatus(statusCode) {
		return true
	}
	return includeTransient && isOpenAITransientProcessingError(statusCode, upstreamMsg, responseBody)
}

func (s *RateLimitService) handleOpenAICompatDownstreamCapacityPenalty(ctx context.Context, account *Account, statusCode int, responseBody []byte, requestedModel string) bool {
	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	if !tkSkipOpenAIDownstreamCapacityPenalty(account, statusCode, upstreamMsg, responseBody) {
		return false
	}
	reason := tkOpenAICompatDownstreamCapacityReason(statusCode, upstreamMsg, responseBody)
	slog.Info("openai_compat_downstream_capacity_skip_penalty",
		"account_id", account.ID,
		"platform", account.Platform,
		"status_code", statusCode,
		"reason", reason)
	if s != nil {
		satCount := s.recordOpenAIStubSaturation(ctx, account.ID, statusCode, reason)
		s.tkTryOpenAIMirrorModelCooldownOnDownstreamEmpty(ctx, account, satCount, requestedModel)
	}
	return true
}

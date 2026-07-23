package service

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// grok429ShouldUseTKTempUnscheduleFallback keeps headerless 429 on the TokenKey
// temp-unschedule path instead of upstream durable rate-limit state.
func grok429ShouldUseTKTempUnscheduleFallback(snapshot *xai.QuotaSnapshot) bool {
	if snapshot == nil || snapshot.StatusCode != http.StatusTooManyRequests {
		return false
	}
	return !snapshot.HeadersObserved &&
		snapshot.RetryAfterSeconds == nil &&
		snapshot.Requests == nil &&
		snapshot.Tokens == nil
}

// tkHandleGrok429UpstreamError applies TokenKey fallback temp-unschedule when
// upstream quota headers do not establish an active limit window.
func (s *OpenAIGatewayService) tkHandleGrok429UpstreamError(ctx context.Context, account *Account, headers http.Header, responseBody []byte, requestedModel ...string) bool {
	if s == nil || account == nil {
		return false
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	if s.handleOpenAICompatRelayDownstreamCapacityError(stateCtx, account, http.StatusTooManyRequests, responseBody, tkFirstRequestedModel(requestedModel)) {
		return true
	}

	now := time.Now()
	snapshot := parseGrokQuotaSnapshot(headers, http.StatusTooManyRequests, now)
	_, hasActiveLimit := grokRateLimitResetAtForAccount(account, snapshot, now)
	if grok429ShouldUseTKTempUnscheduleFallback(snapshot) {
		hasActiveLimit = false
	}
	if hasActiveLimit {
		return false
	}

	cooldown := openAIOAuth429FallbackCooldown
	if s.rateLimitService != nil {
		if resetAt := s.rateLimitService.calculateOpenAI429ResetTime(headers); resetAt != nil && resetAt.After(now) {
			s.tempUnscheduleGrok(stateCtx, account, resetAt.Sub(now), "grok rate limited")
			return true
		}
		if configured, ok := s.rateLimitService.get429FallbackCooldown(stateCtx, account); ok && configured > 0 {
			cooldown = configured
		}
	}
	s.tempUnscheduleGrok(stateCtx, account, cooldown, "grok rate limited")
	return true
}

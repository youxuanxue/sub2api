package service

import (
	"context"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

// kiroPromptUsage resolves prompt-side tokens for billing. When
// gateway.kiro_cache_billing.enabled is false, the full estimate stays in
// InputTokens. When enabled (default), matching conversation prefixes become
// cache_read (with haircut).
//
// cacheBillingEnabled reflects the kill-switch only (not session readiness) so
// BillingTier stays kiro-cache-estimated whenever the feature is on — even if
// conversationId/store cannot split this request.
func (s *KiroGatewayService) kiroPromptUsage(
	ctx context.Context,
	account *Account,
	req *kiroproto.ClaudeRequest,
	payload *kiroproto.KiroPayload,
) (inputTokens, cacheReadTokens, cacheCreationTokens int, sessionKey string, cacheBillingEnabled bool) {
	inputTokens = kiroproto.EstimateInputTokens(req)
	if s == nil || req == nil || account == nil {
		return inputTokens, 0, 0, "", false
	}
	if !s.kiroCacheBillingFlagEnabled(ctx) {
		return inputTokens, 0, 0, "", false
	}
	cacheBillingEnabled = true
	conversationID := ""
	if payload != nil {
		conversationID = payload.ConversationState.ConversationID
	}
	sessionKey = kiroproto.CacheSessionKey(account.ID, req.Model, conversationID)
	if sessionKey == "" || s.kiroCacheStore == nil {
		return inputTokens, 0, 0, "", true
	}
	split := kiroproto.EstimateCacheUsageSplit(ctx, s.kiroCacheStore, sessionKey, req)
	return split.InputTokens, split.CacheReadTokens, split.CacheCreationTokens, sessionKey, true
}

func (s *KiroGatewayService) kiroCacheBillingFlagEnabled(ctx context.Context) bool {
	if s == nil || s.kiroCacheBillingSetting == nil {
		return true
	}
	return s.kiroCacheBillingSetting.IsKiroCacheBillingEnabled(ctx)
}

func (s *KiroGatewayService) commitKiroCacheFingerprints(
	ctx context.Context,
	sessionKey string,
	req *kiroproto.ClaudeRequest,
	rawBody []byte,
	enabled bool,
) {
	if !enabled || s == nil || s.kiroCacheStore == nil || sessionKey == "" || req == nil {
		return
	}
	ttl := kiroproto.ResolveCacheTTL(rawBody)
	kiroproto.CommitCacheFingerprints(ctx, s.kiroCacheStore, sessionKey, req, ttl)
}

func kiroClaudeUsage(input, output, cacheRead, cacheCreation int) ClaudeUsage {
	return ClaudeUsage{
		InputTokens:              input,
		OutputTokens:             output,
		CacheReadInputTokens:     cacheRead,
		CacheCreationInputTokens: cacheCreation,
	}
}

func kiroBillingTier(cacheBillingEnabled bool) string {
	if cacheBillingEnabled {
		return kiroproto.KiroCacheEstimatedBillingTier
	}
	return kiroproto.KiroEstimatedBillingTier
}

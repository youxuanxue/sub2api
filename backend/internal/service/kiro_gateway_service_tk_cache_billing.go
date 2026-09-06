package service

import (
	"context"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

// kiroPromptUsage resolves prompt-side tokens for billing. When the opt-in
// cache-billing flag is off (default), the full estimate stays in InputTokens.
// When on, matching conversation prefixes become cache_read (with haircut).
func (s *KiroGatewayService) kiroPromptUsage(
	ctx context.Context,
	account *Account,
	req *kiroproto.ClaudeRequest,
	payload *kiroproto.KiroPayload,
) (inputTokens, cacheReadTokens, cacheCreationTokens int, sessionKey string, ttlEnabled bool) {
	inputTokens = kiroproto.EstimateInputTokens(req)
	if s == nil || req == nil || account == nil {
		return inputTokens, 0, 0, "", false
	}
	if s.tkSettingService != nil && !s.tkSettingService.IsKiroCacheBillingEnabled(ctx) {
		return inputTokens, 0, 0, "", false
	}
	conversationID := ""
	if payload != nil {
		conversationID = payload.ConversationState.ConversationID
	}
	sessionKey = kiroproto.CacheSessionKey(account.ID, req.Model, conversationID)
	if sessionKey == "" || s.kiroCacheStore == nil {
		return inputTokens, 0, 0, "", false
	}
	split := kiroproto.EstimateCacheUsageSplit(ctx, s.kiroCacheStore, sessionKey, req)
	return split.InputTokens, split.CacheReadTokens, split.CacheCreationTokens, sessionKey, true
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

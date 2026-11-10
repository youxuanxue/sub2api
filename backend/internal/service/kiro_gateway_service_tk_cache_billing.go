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
//
// est is the per-request PromptEstimate; pass the same value to
// commitKiroCacheFingerprints so the input total, cache split and fingerprint
// commit share one tokenization of the prompt.
func (s *KiroGatewayService) kiroPromptUsage(
	ctx context.Context,
	account *Account,
	est *kiroproto.PromptEstimate,
	payload *kiroproto.KiroPayload,
) (inputTokens, cacheReadTokens, cacheCreationTokens int, sessionKey string, cacheBillingEnabled bool) {
	inputTokens = est.InputTokens()
	req := est.Request()
	if s == nil || req == nil || account == nil {
		return inputTokens, 0, 0, "", false
	}
	if !s.kiroCacheBillingFlagEnabled(ctx) {
		return inputTokens, 0, 0, "", false
	}
	conversationID := ""
	if payload != nil {
		conversationID = payload.ConversationState.ConversationID
	}
	sessionKey = kiroproto.CacheSessionKey(account.ID, req.Model, conversationID)
	if sessionKey == "" || s.kiroCacheStore == nil {
		return inputTokens, 0, 0, sessionKey, true
	}
	split := est.CacheUsageSplit(ctx, s.kiroCacheStore, sessionKey)
	return split.InputTokens, split.CacheReadTokens, split.CacheCreationTokens, sessionKey, true
}

func (s *KiroGatewayService) kiroCacheBillingFlagEnabled(ctx context.Context) bool {
	if s == nil || s.kiroCacheBillingSetting == nil {
		return true
	}
	return s.kiroCacheBillingSetting.IsKiroCacheBillingEnabled(ctx)
}

// commitKiroCacheFingerprints reuses the prefix blocks kiroPromptUsage already
// built on est instead of re-tokenizing every segment.
func (s *KiroGatewayService) commitKiroCacheFingerprints(
	ctx context.Context,
	sessionKey string,
	est *kiroproto.PromptEstimate,
	rawBody []byte,
	enabled bool,
) {
	if !enabled || s == nil || s.kiroCacheStore == nil || sessionKey == "" || est.Request() == nil {
		return
	}
	ttl := kiroproto.ResolveCacheTTL(rawBody)
	est.CommitCacheFingerprints(ctx, s.kiroCacheStore, sessionKey, ttl)
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

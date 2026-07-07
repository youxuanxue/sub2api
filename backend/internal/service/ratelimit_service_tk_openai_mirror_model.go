package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// TK — prod-side per-model cooldown for header-less edge empty-pool 429s on OpenAI
// edge-mirror stubs (openai-us* → api-us*.tokenkey.dev).
//
// Mirrors ratelimit_service_tk_mirror_class_429.go for the GPT line: when edge
// spark (or another mapped model) is dry but gpt-5.4 still has headroom on the
// same edge, prod must not treat the whole mirror stub as dead for every model.
// The edge's relayed 429 carries no model scope, so prod infers it from the
// in-flight requested model and writes a bounded model_rate_limits floor on the
// mirror account only — sibling models on that stub stay schedulable.

const tkOpenAIMirrorDownstreamEmptyReason = "429_openai_mirror_downstream_empty"

// tkTryOpenAIMirrorModelCooldownOnDownstreamEmpty writes a model-scoped cooldown
// on a prod OpenAI edge-mirror stub when a downstream empty-pool envelope is
// SUSTAINED for the in-flight model. Returns true iff a model cooldown was written.
func (s *RateLimitService) tkTryOpenAIMirrorModelCooldownOnDownstreamEmpty(
	ctx context.Context,
	account *Account,
	saturationCount int64,
	requestedModel string,
) bool {
	if s == nil || s.accountRepo == nil || account == nil {
		return false
	}
	if !tkIsOpenAIEdgeMirrorStub(account) {
		return false
	}
	if saturationCount < edgeMirrorStubSaturationThreshold {
		return false
	}
	scopeKey := strings.TrimSpace(account.GetMappedModel(requestedModel))
	if scopeKey == "" {
		return false
	}
	if rem := account.GetModelRateLimitRemainingTimeWithContext(ctx, requestedModel); rem > tkMirrorClassCooldownRewriteFloor {
		return false
	}
	resetAt := time.Now().Add(time.Duration(edgeMirrorStubSaturationWindowSeconds) * time.Second)
	s.notifyAccountSchedulingBlocked(account, resetAt, tkOpenAIMirrorDownstreamEmptyReason, scopeKey)
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, scopeKey, resetAt, tkOpenAIMirrorDownstreamEmptyReason); err != nil {
		slog.Warn("openai_mirror_model_cooldown_failed",
			"account_id", account.ID,
			"scope", scopeKey,
			"error", err)
		return false
	}
	slog.Info("openai_mirror_model_rate_limited",
		"account_id", account.ID,
		"scope", scopeKey,
		"requested_model", requestedModel,
		"reset_at", resetAt,
		"reset_in", time.Until(resetAt).Truncate(time.Second),
		"saturation_count", saturationCount)
	return true
}

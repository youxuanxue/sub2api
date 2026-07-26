package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

const tkAntigravityRelayDownstreamEmptyReason = "429_antigravity_relay_downstream_empty"

func (s *RateLimitService) SetAntigravitySaturationCounter(cache AntigravitySaturationCounterCache) {
	s.antigravitySaturationCounter = cache
}

func (s *RateLimitService) recordAntigravityRelaySaturation(
	ctx context.Context,
	accountID int64,
	modelKey string,
	statusCode int,
) int64 {
	if s == nil || s.antigravitySaturationCounter == nil {
		return 0
	}
	count, err := s.antigravitySaturationCounter.IncrementSaturation(
		ctx,
		accountID,
		modelKey,
		edgeMirrorStubSaturationWindowSeconds,
	)
	if err != nil {
		slog.Warn("antigravity_relay_saturation_increment_failed",
			"account_id", accountID,
			"model_key", modelKey,
			"error", err)
		return 0
	}
	if count == edgeMirrorStubSaturationThreshold {
		slog.Info("antigravity_relay_saturated",
			"account_id", accountID,
			"model_key", modelKey,
			"recent_count", count,
			"threshold", edgeMirrorStubSaturationThreshold,
			"window_seconds", edgeMirrorStubSaturationWindowSeconds,
			"status_code", statusCode)
	}
	return count
}

func (s *RateLimitService) tkTryAntigravityRelayModelCooldown(
	ctx context.Context,
	account *Account,
	saturationCount int64,
	requestedModel string,
	scopeKey string,
) bool {
	if s == nil || s.accountRepo == nil || !tkIsAntigravityEdgeRelayStub(account) {
		return false
	}
	if saturationCount < edgeMirrorStubSaturationThreshold {
		return false
	}
	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		return false
	}
	if rem := account.GetModelRateLimitRemainingTimeWithContext(ctx, requestedModel); rem > tkMirrorClassCooldownRewriteFloor {
		return false
	}
	resetAt := time.Now().Add(time.Duration(edgeMirrorStubSaturationWindowSeconds) * time.Second)
	if err := s.accountRepo.SetModelRateLimit(
		ctx,
		account.ID,
		scopeKey,
		resetAt,
		tkAntigravityRelayDownstreamEmptyReason,
	); err != nil {
		slog.Warn("antigravity_relay_model_cooldown_failed",
			"account_id", account.ID,
			"scope", scopeKey,
			"error", err)
		return false
	}
	slog.Info("antigravity_relay_model_rate_limited",
		"account_id", account.ID,
		"scope", scopeKey,
		"requested_model", requestedModel,
		"reset_at", resetAt,
		"reset_in", time.Until(resetAt).Truncate(time.Second),
		"saturation_count", saturationCount)
	return true
}

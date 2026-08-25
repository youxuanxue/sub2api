package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	tkCloudwiseProvider424Reason          = "cloudwise_provider_error_424"
	tkCloudwiseProvider424ModelCooldown   = 15 * time.Minute
	tkCloudwiseProvider424AccountCooldown = 30 * time.Minute
)

func tkIsCloudwiseProvider424Response(account *Account, statusCode int, responseBody []byte) bool {
	if account == nil || statusCode != http.StatusFailedDependency || !isCloudwiseRelayAccount(account) {
		return false
	}
	return strings.Contains(strings.ToLower(string(responseBody)), "provider error")
}

func tkHasOtherCloudwiseProvider424ModelCooldown(account *Account, currentScope string, now time.Time) bool {
	if account == nil {
		return false
	}
	for scope, cooldown := range account.ActiveModelRateLimits(now) {
		if scope != currentScope && cooldown.Reason == tkCloudwiseProvider424Reason {
			return true
		}
	}
	return false
}

func (s *RateLimitService) tkApplyCloudwiseProvider424AccountCooldown(
	ctx context.Context,
	account *Account,
	responseBody []byte,
	reason string,
) {
	rule := TempUnschedulableRule{DurationMinutes: int(tkCloudwiseProvider424AccountCooldown / time.Minute)}
	if !s.triggerTempUnschedulable(
		ctx,
		account,
		rule,
		-1,
		http.StatusFailedDependency,
		tkCloudwiseProvider424Reason,
		responseBody,
	) {
		slog.Warn("cloudwise_provider_424_account_cooldown_failed",
			"account_id", account.ID,
			"reason", reason)
		return
	}
	slog.Warn("cloudwise_provider_424_account_cooled",
		"account_id", account.ID,
		"cooldown", tkCloudwiseProvider424AccountCooldown,
		"reason", reason)
}

func (s *RateLimitService) tkTryCloudwiseProvider424Cooldown(
	ctx context.Context,
	account *Account,
	statusCode int,
	responseBody []byte,
	requestedModel string,
) bool {
	if s == nil || s.accountRepo == nil || !tkIsCloudwiseProvider424Response(account, statusCode, responseBody) {
		return false
	}

	scopeKey := strings.TrimSpace(account.GetMappedModel(requestedModel))
	if scopeKey == "" {
		s.tkApplyCloudwiseProvider424AccountCooldown(ctx, account, responseBody, "missing_model_scope")
		return true
	}

	now := time.Now()
	resetAt := now.Add(tkCloudwiseProvider424ModelCooldown)
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, scopeKey, resetAt, tkCloudwiseProvider424Reason); err != nil {
		slog.Warn("cloudwise_provider_424_model_cooldown_failed",
			"account_id", account.ID,
			"model", scopeKey,
			"error", err)
		s.tkApplyCloudwiseProvider424AccountCooldown(ctx, account, responseBody, "model_cooldown_write_failed")
		return true
	}

	slog.Warn("cloudwise_provider_424_model_cooled",
		"account_id", account.ID,
		"model", scopeKey,
		"reset_at", resetAt,
		"cooldown", tkCloudwiseProvider424ModelCooldown)

	latest := account
	if refreshed, err := s.accountRepo.GetByID(ctx, account.ID); err != nil {
		slog.Warn("cloudwise_provider_424_account_refresh_failed",
			"account_id", account.ID,
			"model", scopeKey,
			"error", err)
	} else if refreshed != nil {
		latest = refreshed
	}
	if tkHasOtherCloudwiseProvider424ModelCooldown(latest, scopeKey, now) {
		s.tkApplyCloudwiseProvider424AccountCooldown(ctx, account, responseBody, "distinct_model_424")
	}
	return true
}

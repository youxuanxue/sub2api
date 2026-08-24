package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	tkCloudwiseModelBalanceCooldownReason = "cloudwise_model_insufficient_balance_402"
	tkCloudwiseModelBalanceCooldown       = 5 * time.Hour
)

func tkIsCloudwiseModelBalance402(account *Account, statusCode int, responseBody []byte, requestedModel string) bool {
	if account == nil || statusCode != http.StatusPaymentRequired || !isCloudwiseRelayAccount(account) {
		return false
	}
	if strings.TrimSpace(requestedModel) == "" {
		return false
	}
	body := strings.ToLower(strings.TrimSpace(string(responseBody)))
	return strings.Contains(body, "insufficient balance") || strings.Contains(body, "insufficient_quota")
}

func (s *RateLimitService) tkTryCloudwiseModelBalanceCooldown(
	ctx context.Context,
	account *Account,
	statusCode int,
	responseBody []byte,
	requestedModel string,
) bool {
	if s == nil || s.accountRepo == nil || !tkIsCloudwiseModelBalance402(account, statusCode, responseBody, requestedModel) {
		return false
	}
	scopeKey := strings.TrimSpace(account.GetMappedModel(requestedModel))
	if scopeKey == "" {
		return false
	}

	resetAt := time.Now().Add(tkCloudwiseModelBalanceCooldown)
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, scopeKey, resetAt, tkCloudwiseModelBalanceCooldownReason); err != nil {
		slog.Warn("cloudwise_model_balance_rate_limit_failed",
			"account_id", account.ID,
			"model", scopeKey,
			"error", err)
		return false
	}
	s.notifyAccountSchedulingBlocked(account, resetAt, "402_cloudwise_model_balance", scopeKey)
	slog.Info("cloudwise_model_balance_rate_limited",
		"account_id", account.ID,
		"model", scopeKey,
		"reset_at", resetAt,
		"reset_in", time.Until(resetAt).Truncate(time.Second))
	return true
}

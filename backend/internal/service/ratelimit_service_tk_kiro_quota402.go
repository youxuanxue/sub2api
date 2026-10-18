package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const tkKiroQuotaLimitIncidentReason = "kiro_quota_limit"

const tkKiroQuotaLimitMarker = "reached the limit"

func tkIsKiroQuotaLimit402(account *Account, statusCode int, upstreamMsg string, responseBody []byte) bool {
	if account == nil || account.Platform != PlatformKiro || statusCode != http.StatusPaymentRequired {
		return false
	}
	haystack := strings.ToLower(strings.TrimSpace(upstreamMsg))
	if haystack == "" {
		haystack = strings.ToLower(strings.TrimSpace(string(responseBody)))
	}
	return strings.Contains(haystack, tkKiroQuotaLimitMarker)
}

func (s *RateLimitService) tkHandleKiroQuotaLimit402(ctx context.Context, account *Account, upstreamMsg string, responseBody []byte) bool {
	if s == nil || account == nil {
		return false
	}
	if !tkIsKiroQuotaLimit402(account, http.StatusPaymentRequired, upstreamMsg, responseBody) {
		return false
	}
	msg := "Payment required (402): insufficient balance or billing issue"
	if trimmed := strings.TrimSpace(upstreamMsg); trimmed != "" {
		msg = "Payment required (402): " + trimmed
	} else if body := strings.TrimSpace(string(responseBody)); body != "" {
		msg = "Payment required (402): " + body
	}
	s.notifyAccountSchedulingBlocked(account, time.Time{}, tkKiroQuotaLimitIncidentReason, msg)
	if err := s.accountRepo.SetError(ctx, account.ID, msg); err != nil {
		slog.Warn("kiro_quota_limit_set_error_failed", "account_id", account.ID, "error", err)
		return true
	}
	slog.Warn("account_disabled_kiro_quota_limit", "account_id", account.ID, "error", msg)
	return true
}

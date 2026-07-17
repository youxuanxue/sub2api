package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// TK: Kiro (sixth platform) upstream HTTP 402 "You have reached the limit."
//
// Prod incident 2026-06-25 (edge-us4, account id=9): Kiro OAuth subscription
// quota exhaustion arrives as 402 + plain-text "You have reached the limit."
// Generic case 402 already SetError + notifyAccountSchedulingBlocked, but it
// used reason "auth_error" ("账号永久失效（认证失败）") — misleading for a quota
// cap, and admin account-test bypassed HandleUpstreamError entirely (same class
// of gap as newapi bridge #617 / #628). This companion narrows on the Kiro
// quota marker and routes through reason "kiro_quota_limit" for an immediate,
// actionable P0 Feishu card while keeping the same stop-scheduling semantics.

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

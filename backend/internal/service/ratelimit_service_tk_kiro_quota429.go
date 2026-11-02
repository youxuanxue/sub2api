package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	tkKiroEndpointQuotaExhaustedCooldown = 10 * time.Second
	tkKiroEndpointQuotaExhaustedReason   = "kiro_endpoint_quota_exhausted"
	// Public wire copy is TkClientFacingBusy429Message (SSOT). This constant is
	// the INTERNAL ops/detail phrase only.
	tkKiroEndpointQuotaExhaustedClient = "Kiro upstream quota exhausted, please retry later"
)

// KiroEndpointQuotaExhaustedRetryAfterSeconds is the HTTP Retry-After value paired
// with tkKiroEndpointQuotaExhaustedCooldown for gateway 429 responses.
func KiroEndpointQuotaExhaustedRetryAfterSeconds() int {
	return int(tkKiroEndpointQuotaExhaustedCooldown / time.Second)
}

func tkIsKiroEndpointQuotaExhausted(upstreamMsg string, responseBody []byte) bool {
	haystack := strings.ToLower(strings.TrimSpace(upstreamMsg) + "\n" + strings.TrimSpace(string(responseBody)))
	if strings.Contains(haystack, "quota exhausted on") {
		return true
	}
	return strings.Contains(haystack, strings.ToLower(tkKiroEndpointQuotaExhaustedClient))
}

func tkIsKiroModelTemporarilyRestricted(upstreamMsg string, responseBody []byte) bool {
	return isKiroModelTemporarilyRestrictedText(strings.TrimSpace(upstreamMsg) + "\n" + strings.TrimSpace(string(responseBody)))
}

func (s *RateLimitService) tkMaybeHandleKiroModelTemporarilyRestricted(
	ctx context.Context,
	account *Account,
	upstreamMsg string,
	responseBody []byte,
	requestedModel string,
) (handled bool, shouldDisable bool) {
	if s == nil || account == nil || account.Platform != PlatformKiro || !tkIsKiroModelTemporarilyRestricted(upstreamMsg, responseBody) {
		return false, false
	}
	modelKey := modelRateLimitKeyForUpstreamModelNotFound(ctx, account, requestedModel)
	if modelKey == "" {
		return true, false
	}
	now := time.Now()
	resetAt := now.Add(kiroModelTemporarilyRestrictedCooldown)
	setAccountModelRateLimitSnapshot(account, modelKey, resetAt, kiroModelTemporarilyRestrictedReason, now)
	if s.accountRepo == nil {
		return true, false
	}
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, modelKey, resetAt, kiroModelTemporarilyRestrictedReason); err != nil {
		slog.Warn("kiro_model_temporarily_restricted_rate_limit_failed",
			"account_id", account.ID,
			"model", modelKey,
			"error", err,
		)
	} else {
		slog.Info("kiro_model_temporarily_restricted_cooldown",
			"account_id", account.ID,
			"model", modelKey,
			"reset_at", resetAt.UTC().Format(time.RFC3339),
		)
	}
	return true, false
}

func (s *RateLimitService) tkMaybeHandleKiroEndpointQuotaExhausted(
	ctx context.Context,
	account *Account,
	upstreamMsg string,
	responseBody []byte,
) (handled bool, shouldDisable bool) {
	if s == nil || account == nil || !tkIsKiroEndpointQuotaExhausted(upstreamMsg, responseBody) {
		return false, false
	}
	if account.Platform != PlatformKiro {
		return false, false
	}
	return true, s.tkHandleKiroEndpointQuotaExhausted(ctx, account, upstreamMsg)
}

func (s *RateLimitService) tkHandleKiroEndpointQuotaExhausted(ctx context.Context, account *Account, upstreamMsg string) bool {
	if s == nil || account == nil {
		return false
	}
	until := time.Now().Add(tkKiroEndpointQuotaExhaustedCooldown)
	msg := "Kiro upstream endpoint quota exhausted; short cooldown"
	if trimmed := strings.TrimSpace(upstreamMsg); trimmed != "" {
		msg = msg + ": " + trimmed
	}
	s.notifyAccountSchedulingBlocked(account, until, tkKiroEndpointQuotaExhaustedReason, msg)
	state := &TempUnschedState{
		UntilUnix:       until.Unix(),
		TriggeredAtUnix: time.Now().Unix(),
		StatusCode:      http.StatusTooManyRequests,
		MatchedKeyword:  tkKiroEndpointQuotaExhaustedReason,
		RuleIndex:       -1,
		ErrorMessage:    truncateTempUnschedMessage([]byte(msg), tempUnschedMessageMaxBytes),
	}
	reason := msg
	if raw, marshalErr := json.Marshal(state); marshalErr == nil {
		reason = string(raw)
	}
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("kiro_endpoint_quota_exhausted_set_temp_unschedulable_failed",
			"account_id", account.ID,
			"error", err,
		)
	}
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(ctx, account.ID, state); err != nil {
			slog.Warn("kiro_endpoint_quota_exhausted_temp_unsched_cache_set_failed",
				"account_id", account.ID,
				"error", err,
			)
		}
	}
	slog.Info("kiro_endpoint_quota_exhausted_short_cooldown",
		"account_id", account.ID,
		"platform", account.Platform,
		"until", until.UTC().Format(time.RFC3339),
	)
	return true
}

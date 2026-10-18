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
	tkKiroEndpointQuotaExhaustedClient   = "Kiro upstream quota exhausted, please retry later"
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

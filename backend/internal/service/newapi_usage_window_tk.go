package service

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TK: NewAPI recoverable usage-window snapshots (weekly / 5h / 7d quota text).
//
// Prod 2026-09-02 account 88 volcengine-agent-plan: upstream 429
// "You have exceeded the weekly usage quota. It will reset at …" was correctly
// excluded from standing-billing SetError (tkIsRecoverableUsageWindowMessage),
// but handle429 could not parse OpenAI-shaped resets_at and fell through to a
// few-second fallback cooldown. Admin usage then kept UpstreamQuota.state=
// unsupported ("未接入上游配额") and local 5h/7d bars stayed at utilization 0 —
// operators could not see that the account was weekly-exhausted until reset.
//
// This file is the write+read SSOT for that window text: parse reset time,
// persist Extra, cool until reset, and surface utilization on the local 7d
// (or 5h) progress + UpstreamQuota dimensions.
//
// Prod 2026-09-19 edge-us5 account 17 volcengine-agent-plan: the same weekly
// prose arrived on kimi-k3 while deepseek-v4-flash still completed. Agent Plan
// windows are per model, so that path uses SetModelRateLimit and must not
// paint the account 7d bar or SetRateLimited.

const (
	newAPIWeeklyUtilExtraKey      = "newapi_weekly_utilization"
	newAPIWeeklyResetExtraKey     = "newapi_weekly_reset"
	newAPIWeeklySampledExtraKey   = "newapi_weekly_sampled_at"
	newAPIFiveHourUtilExtraKey    = "newapi_5h_utilization"
	newAPIFiveHourResetExtraKey   = "newapi_5h_reset"
	newAPIFiveHourSampledExtraKey = "newapi_5h_sampled_at"
	newAPISevenDayUtilExtraKey    = "newapi_7d_utilization"
	newAPISevenDayResetExtraKey   = "newapi_7d_reset"
	newAPISevenDaySampledExtraKey = "newapi_7d_sampled_at"

	newAPIUpstreamWeeklyKey   = "newapi_weekly"
	newAPIUpstreamFiveHourKey = "newapi_5h"
	newAPIUpstreamSevenDayKey = "newapi_7d"

	// tkNewAPIModelWindowReason is a model-scoped cooldown. It must stay out of
	// isWholeAccountRuntimeBlockReason so one model's weekly/5h quota does not
	// block the rest of the account.
	tkNewAPIModelWindowReason = "429_newapi_model_window"
)

var newAPIUsageWindowResetAtRE = regexp.MustCompile(`(?i)it will reset at\s+(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\s+[+-]\d{4}(?:\s+\S+)?)`)
var newAPIUsageWindowShortResetAtRE = regexp.MustCompile(`(?i)(?:it|the quota) will reset at\s+(\d{2}-\d{2} \d{2}:\d{2}:\d{2}) UTC\b`)

type newAPIUsageWindowHit struct {
	Window  string // "weekly" | "5h" | "7d"
	ResetAt time.Time
}

// tkParseNewAPIUsageWindowResponse extracts quota exhaustion and its reset
// from the upstream message and standard HTTP retry headers.
func tkParseNewAPIUsageWindowResponse(haystack string, headers http.Header, now time.Time) *newAPIUsageWindowHit {
	haystack = strings.ToLower(strings.TrimSpace(haystack))
	if haystack == "" || !tkIsRecoverableUsageWindowMessage(haystack) {
		return nil
	}
	if !strings.Contains(haystack, "exhausted") && !strings.Contains(haystack, "exceeded") {
		return nil
	}
	window := "weekly"
	switch {
	case strings.Contains(haystack, "5-hour") || strings.Contains(haystack, "5 hour"):
		window = "5h"
	case strings.Contains(haystack, "7-day") || strings.Contains(haystack, "7 day"):
		window = "7d"
	case strings.Contains(haystack, "weekly"):
		window = "weekly"
	}
	// Date anchors relative Retry-After and yearless timestamps to the provider clock.
	reference := now
	if date, err := http.ParseTime(headers.Get("Date")); err == nil {
		reference = date
	}
	resetAt, ok := tkParseNewAPIUsageWindowResetAt(haystack)
	if retryAt := parseRetryAfterResetTime(headers, reference); retryAt != nil && retryAt.After(now) {
		resetAt, ok = *retryAt, true
	}
	if !ok {
		if m := newAPIUsageWindowShortResetAtRE.FindStringSubmatch(haystack); len(m) == 2 {
			// A yearless reset must be within this quota window. In particular an
			// expired September reset must not become a year-long cooldown.
			maxWindow := 7 * 24 * time.Hour
			if window == "5h" {
				maxWindow = 5 * time.Hour
			}
			for _, year := range []int{reference.UTC().Year(), reference.UTC().Year() + 1} {
				candidate, err := time.Parse("2006-01-02 15:04:05", strconv.Itoa(year)+"-"+m[1])
				if err == nil && candidate.After(reference) && candidate.Sub(reference) <= maxWindow && candidate.After(now) {
					resetAt, ok = candidate, true
					break
				}
			}
		}
	}
	if !ok {
		return nil
	}
	return &newAPIUsageWindowHit{Window: window, ResetAt: resetAt}
}

func tkParseNewAPIUsageWindowResetAt(haystack string) (time.Time, bool) {
	m := newAPIUsageWindowResetAtRE.FindStringSubmatch(haystack)
	if len(m) < 2 {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(m[1])
	for _, layout := range []string{
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05 -0700",
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	// Drop trailing zone name when layout without MST fails on unknown abbrev.
	if parts := strings.Fields(raw); len(parts) >= 3 {
		trimmed := strings.Join(parts[:3], " ")
		if t, err := time.Parse("2006-01-02 15:04:05 -0700", trimmed); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// tkTryHandleNewAPIUsageWindow429 cools until the upstream window reset.
// VolcEngine Agent Plan quotas are per model: a kimi-k3 weekly 429 must not
// SetRateLimited the whole account or paint the account 7d bar at 100%.
// Ali token-plan and other NewAPI windows stay account-wide.
// Returns true when the 429 was a recoverable usage-window hit (caller must not
// fall through to the short fallback cooldown).
func (s *RateLimitService) tkTryHandleNewAPIUsageWindow429(ctx context.Context, account *Account, headers http.Header, responseBody []byte, requestedModel ...string) bool {
	if s == nil || account == nil || account.Platform != PlatformNewAPI || s.accountRepo == nil {
		return false
	}
	msg := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	if msg == "" {
		msg = string(responseBody)
	}
	hit := tkParseNewAPIUsageWindowResponse(msg, headers, time.Now())
	if hit == nil {
		return false
	}
	if !hit.ResetAt.After(time.Now()) {
		return false
	}
	if scope := tkVolcAgentPlanUsageWindowModelScope(account, firstRequestedModel(requestedModel)); scope != "" {
		return s.persistVolcAgentPlanModelUsageWindow(ctx, account, hit, scope)
	}

	s.persistNewAPIUsageWindowSnapshot(ctx, account, hit)
	s.notifyAccountSchedulingBlocked(account, hit.ResetAt, "429", "newapi_"+hit.Window+"_window_exhausted")
	if err := s.accountRepo.SetRateLimited(ctx, account.ID, hit.ResetAt); err != nil {
		slog.Warn("newapi_usage_window_set_rate_limited_failed",
			"account_id", account.ID, "window", hit.Window, "error", err)
		return true
	}
	slog.Info("newapi_account_usage_window_rate_limited",
		"account_id", account.ID,
		"window", hit.Window,
		"reset_at", hit.ResetAt,
		"reset_in", time.Until(hit.ResetAt).Truncate(time.Second),
	)
	return true
}

func tkVolcAgentPlanUsageWindowModelScope(account *Account, requestedModel string) string {
	if !isNewAPIVolcEngineAgentPlanAccount(account) {
		return ""
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ""
	}
	if mapped := strings.TrimSpace(account.GetMappedModel(requestedModel)); mapped != "" {
		return mapped
	}
	return requestedModel
}

func (s *RateLimitService) persistVolcAgentPlanModelUsageWindow(ctx context.Context, account *Account, hit *newAPIUsageWindowHit, scope string) bool {
	resetAt := hit.ResetAt
	if existing := account.modelRateLimitResetAt(scope); existing != nil && existing.After(resetAt) {
		resetAt = *existing
	}
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, scope, resetAt, tkNewAPIModelWindowReason); err != nil {
		slog.Warn("newapi_model_usage_window_set_rate_limited_failed",
			"account_id", account.ID, "model", scope, "window", hit.Window, "error", err)
		return true
	}
	setAccountModelRateLimitSnapshot(account, scope, resetAt, tkNewAPIModelWindowReason, time.Now())
	s.notifyAccountSchedulingBlocked(account, resetAt, tkNewAPIModelWindowReason, scope+"·"+hit.Window)
	if volcAgentPlanAccountWindowLockIgnored(account) {
		if err := s.accountRepo.ClearRateLimit(ctx, account.ID); err != nil {
			slog.Warn("volc_agent_plan_clear_false_account_window_lock_failed",
				"account_id", account.ID, "model", scope, "error", err)
		} else {
			account.RateLimitedAt = nil
			account.RateLimitResetAt = nil
			s.clearNewAPIUsageWindowSnapshot(ctx, account)
		}
	}
	slog.Info("newapi_model_usage_window_rate_limited",
		"account_id", account.ID,
		"model", scope,
		"window", hit.Window,
		"reset_at", resetAt,
		"reset_in", time.Until(resetAt).Truncate(time.Second),
	)
	return true
}

// VolcAgentPlanAccountWindowLockIgnored reports an account-wide rate limit that
// was written from a VolcEngine Agent Plan per-model usage window. Other models
// on the same account keep working, so scheduling and the admin badge must not
// treat it as a whole-account 429.
func VolcAgentPlanAccountWindowLockIgnored(account *Account) bool {
	return volcAgentPlanAccountWindowLockIgnored(account)
}

func volcAgentPlanAccountWindowLockIgnored(account *Account) bool {
	if account == nil || account.RateLimitResetAt == nil || !time.Now().Before(*account.RateLimitResetAt) {
		return false
	}
	if !isNewAPIVolcEngineAgentPlanAccount(account) {
		return false
	}
	resetUnix := float64(account.RateLimitResetAt.Unix())
	for _, key := range []string{newAPIWeeklyResetExtraKey, newAPIFiveHourResetExtraKey, newAPISevenDayResetExtraKey} {
		raw := parseExtraFloat64(account.Extra[key])
		if raw > 0 && math.Abs(raw-resetUnix) < 2 {
			return true
		}
	}
	return false
}

func accountWideRateLimitBlocks(account *Account, now time.Time) bool {
	if account == nil || account.RateLimitResetAt == nil || !now.Before(*account.RateLimitResetAt) {
		return false
	}
	return !volcAgentPlanAccountWindowLockIgnored(account)
}

func (s *RateLimitService) clearNewAPIUsageWindowSnapshot(ctx context.Context, account *Account) {
	if s == nil || s.accountRepo == nil || account == nil {
		return
	}
	updates := map[string]any{
		newAPIWeeklyUtilExtraKey:    0.0,
		newAPIWeeklyResetExtraKey:   0.0,
		newAPIFiveHourUtilExtraKey:  0.0,
		newAPIFiveHourResetExtraKey: 0.0,
		newAPISevenDayUtilExtraKey:  0.0,
		newAPISevenDayResetExtraKey: 0.0,
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("newapi_usage_window_extra_clear_failed", "account_id", account.ID, "error", err)
		return
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	for k, v := range updates {
		account.Extra[k] = v
	}
}

func (s *RateLimitService) persistNewAPIUsageWindowSnapshot(ctx context.Context, account *Account, hit *newAPIUsageWindowHit) {
	if s == nil || s.accountRepo == nil || account == nil || hit == nil {
		return
	}
	utilKey, resetKey, sampledKey := newAPIUsageWindowExtraKeys(hit.Window)
	if utilKey == "" {
		return
	}
	now := time.Now().UTC()
	updates := map[string]any{
		utilKey:    1.0,
		resetKey:   float64(hit.ResetAt.Unix()),
		sampledKey: now.Format(time.RFC3339Nano),
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("newapi_usage_window_extra_persist_failed",
			"account_id", account.ID, "window", hit.Window, "error", err)
		return
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	for k, v := range updates {
		account.Extra[k] = v
	}
}

func newAPIUsageWindowExtraKeys(window string) (utilKey, resetKey, sampledKey string) {
	switch window {
	case "weekly":
		return newAPIWeeklyUtilExtraKey, newAPIWeeklyResetExtraKey, newAPIWeeklySampledExtraKey
	case "5h":
		return newAPIFiveHourUtilExtraKey, newAPIFiveHourResetExtraKey, newAPIFiveHourSampledExtraKey
	case "7d":
		return newAPISevenDayUtilExtraKey, newAPISevenDayResetExtraKey, newAPISevenDaySampledExtraKey
	default:
		return "", "", ""
	}
}

func applyNewAPIUsageWindowSnapshot(account *Account, usage *UsageInfo) {
	if account == nil || usage == nil || account.Platform != PlatformNewAPI {
		return
	}
	// Agent Plan window hits are per model. An account-level 100% bar would hide
	// models that are still serving.
	if isNewAPIVolcEngineAgentPlanAccount(account) {
		return
	}
	now := time.Now()
	apply := func(window, utilKey, resetKey, dimKey, dimLabel, dimWindow string, progress **UsageProgress) {
		util := parseExtraFloat64(account.Extra[utilKey])
		resetRaw := parseExtraFloat64(account.Extra[resetKey])
		if util <= 0 && resetRaw <= 0 {
			return
		}
		var resetAt *time.Time
		if resetRaw > 0 {
			t := time.Unix(int64(resetRaw), 0)
			if !t.After(now) {
				// Window already rolled; do not keep a stale 100% bar.
				return
			}
			resetAt = &t
		}
		utilization := util * 100
		if *progress == nil {
			*progress = &UsageProgress{}
		}
		(*progress).Utilization = utilization
		(*progress).UtilizationUnknown = false
		if resetAt != nil {
			(*progress).ResetsAt = resetAt
			remaining := int(time.Until(*resetAt).Seconds())
			if remaining < 0 {
				remaining = 0
			}
			(*progress).RemainingSeconds = remaining
		}
		if usage.UpstreamQuota == nil {
			usage.UpstreamQuota = baseUpstreamQuota(PlatformNewAPI, usage, "headers")
		}
		usage.UpstreamQuota.State = "degraded"
		usage.UpstreamQuota.ErrorCode = "rate_limited"
		usage.UpstreamQuota.StatusCode = 429
		d := UpstreamQuotaDimension{
			Key:         dimKey,
			Label:       dimLabel,
			Unit:        "percent",
			Window:      dimWindow,
			Utilization: &utilization,
			ResetsAt:    resetAt,
		}
		// Replace same key if re-applied.
		replaced := false
		for i := range usage.UpstreamQuota.Dimensions {
			if usage.UpstreamQuota.Dimensions[i].Key == dimKey {
				usage.UpstreamQuota.Dimensions[i] = d
				replaced = true
				break
			}
		}
		if !replaced {
			usage.UpstreamQuota.Dimensions = append(usage.UpstreamQuota.Dimensions, d)
		}
	}

	apply("weekly", newAPIWeeklyUtilExtraKey, newAPIWeeklyResetExtraKey, newAPIUpstreamWeeklyKey, "Weekly", "7d", &usage.SevenDay)
	apply("5h", newAPIFiveHourUtilExtraKey, newAPIFiveHourResetExtraKey, newAPIUpstreamFiveHourKey, "5h", "5h", &usage.FiveHour)
	apply("7d", newAPISevenDayUtilExtraKey, newAPISevenDayResetExtraKey, newAPIUpstreamSevenDayKey, "7d", "7d", &usage.SevenDay)
}

func buildNewAPIUpstreamQuota(account *Account, usage *UsageInfo) *UpstreamQuotaInfo {
	info := baseUpstreamQuota(PlatformNewAPI, usage, defaultUsageSource(usage))
	info.State = "unknown"
	if account == nil {
		return info
	}
	// Start from any dimensions already applied onto usage (applyNewAPIUsageWindowSnapshot).
	if usage != nil && usage.UpstreamQuota != nil && len(usage.UpstreamQuota.Dimensions) > 0 {
		info.State = usage.UpstreamQuota.State
		if info.State == "" {
			info.State = "observed"
		}
		info.ErrorCode = usage.UpstreamQuota.ErrorCode
		info.StatusCode = usage.UpstreamQuota.StatusCode
		info.Dimensions = append(info.Dimensions, usage.UpstreamQuota.Dimensions...)
		return info
	}
	// Rebuild from Extra when attachUpstreamQuota runs before apply (or alone).
	tmp := &UsageInfo{Source: defaultUsageSource(usage), UpdatedAt: usage.UpdatedAt}
	applyNewAPIUsageWindowSnapshot(account, tmp)
	if tmp.UpstreamQuota != nil && len(tmp.UpstreamQuota.Dimensions) > 0 {
		return tmp.UpstreamQuota
	}
	info.ErrorCode = ""
	info.Error = ""
	return info
}

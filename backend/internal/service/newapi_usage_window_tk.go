package service

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TK: NewAPI recoverable usage-window snapshots (monthly / weekly / 5h / 7d quota text).
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
// (or 5h) progress + UpstreamQuota dimensions. Monthly quota has its own
// dimension and never overwrites local 5h/7d statistics. Named-model hits use
// model_rate_limits for every NewAPI channel; observations without a model
// retain an explicitly marked account-wide window.

const (
	tkNewAPIModelWindowReason        = "429_newapi_model_window"
	NewAPIAccountWindowLockExtraKey  = "newapi_account_window_lock"
	newAPIQianfanMonthlyQuotaMessage = "token plan person monthly quota limit exceeded"
	newAPIMonthlyUtilExtraKey        = "newapi_monthly_utilization"
	newAPIMonthlyResetExtraKey       = "newapi_monthly_reset"
	newAPIMonthlySampledExtraKey     = "newapi_monthly_sampled_at"
	newAPIWeeklyUtilExtraKey         = "newapi_weekly_utilization"
	newAPIWeeklyResetExtraKey        = "newapi_weekly_reset"
	newAPIWeeklySampledExtraKey      = "newapi_weekly_sampled_at"
	newAPIFiveHourUtilExtraKey       = "newapi_5h_utilization"
	newAPIFiveHourResetExtraKey      = "newapi_5h_reset"
	newAPIFiveHourSampledExtraKey    = "newapi_5h_sampled_at"
	newAPISevenDayUtilExtraKey       = "newapi_7d_utilization"
	newAPISevenDayResetExtraKey      = "newapi_7d_reset"
	newAPISevenDaySampledExtraKey    = "newapi_7d_sampled_at"

	newAPIUpstreamMonthlyKey  = "newapi_monthly"
	newAPIUpstreamWeeklyKey   = "newapi_weekly"
	newAPIUpstreamFiveHourKey = "newapi_5h"
	newAPIUpstreamSevenDayKey = "newapi_7d"
)

var newAPIUsageWindowResetAtRE = regexp.MustCompile(`(?i)it will reset at\s+(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\s+[+-]\d{4}(?:\s+\S+)?)`)
var newAPIUsageWindowShortResetAtRE = regexp.MustCompile(`(?i)(?:it|the quota) will reset at\s+(\d{2}-\d{2} \d{2}:\d{2}:\d{2}) UTC\b`)

type newAPIUsageWindowHit struct {
	Window  string // "monthly" | "weekly" | "5h" | "7d"
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
	case strings.Contains(haystack, "month") || strings.Contains(haystack, "30-day") || strings.Contains(haystack, newAPIQianfanMonthlyQuotaMessage):
		window = "monthly"
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

	if !ok {
		if m := newAPIUsageWindowShortResetAtRE.FindStringSubmatch(haystack); len(m) == 2 {
			// A yearless reset must be within this quota window. In particular an
			// expired September reset must not become a year-long cooldown.
			maxWindow := 7 * 24 * time.Hour
			if window == "monthly" {
				maxWindow = 31 * 24 * time.Hour
			}
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
	// Qianfan omits its reset timestamp. Apply the operational monthly rule:
	// next month's first day at 01:00 Beijing time, ahead of short Retry-After.
	if !ok && strings.Contains(haystack, newAPIQianfanMonthlyQuotaMessage) {
		resetAt, ok = tkQianfanMonthlyResetAt(now), true
	}
	// A monthly quota reset is authoritative even if Retry-After only describes
	// a short request throttle. Preserve existing header precedence for other windows.
	if retryAt := parseRetryAfterResetTime(headers, reference); retryAt != nil && retryAt.After(now) && (window != "monthly" || !ok) {
		resetAt, ok = *retryAt, true
	}
	if !ok {
		return nil
	}
	return &newAPIUsageWindowHit{Window: window, ResetAt: resetAt}
}

func tkQianfanMonthlyResetAt(now time.Time) time.Time {
	beijing := now.In(time.FixedZone("CST", 8*60*60))
	return time.Date(beijing.Year(), beijing.Month()+1, 1, 1, 0, 0, 0, beijing.Location())
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

// tkTryHandleNewAPIUsageWindow429 cools the affected account/model until the
// window reset and records exhaustion for admin usage. Returns true when
// the 429 was a recoverable usage-window hit (caller must not fall through to
// the short fallback cooldown).
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

	// Plan owns the executed model. The fallback is already resolved by native
	// transports; mapping it a second time can cool a different upstream model.
	model := strings.TrimSpace(protocolExecutionResolvedModel(ctx, tempUnschedulableModel(ctx, requestedModel)))
	if model != "" {
		return s.persistNewAPIModelUsageWindow(ctx, account, hit, model)
	}
	if !s.persistNewAPIUsageWindowSnapshot(ctx, account, hit) {
		return false // failed evidence write must not create an unmarked long lock
	}
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

func newAPIModelUsageWindow(reason string) string {
	prefix := tkNewAPIModelWindowReason + ":"
	if !strings.HasPrefix(reason, prefix) {
		return ""
	}
	window := strings.TrimPrefix(reason, prefix)
	switch window {
	case "monthly", "weekly", "5h", "7d":
		return window
	}
	return ""
}

func (s *RateLimitService) persistNewAPIModelUsageWindow(ctx context.Context, account *Account, hit *newAPIUsageWindowHit, model string) bool {
	reset, reason := hit.ResetAt, tkNewAPIModelWindowReason+":"+hit.Window
	if existing, ok := account.ActiveModelRateLimits(time.Now())[model]; ok && existing.RateLimitResetAt.After(reset) {
		reset, reason = existing.RateLimitResetAt, existing.Reason
	}
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, model, reset, reason); err != nil {
		slog.Warn("newapi_model_usage_window_set_rate_limited_failed", "account_id", account.ID, "model", model, "error", err)
		return false
	}
	setAccountModelRateLimitSnapshot(account, model, reset, reason, time.Now())
	s.notifyAccountSchedulingBlocked(account, reset, tkNewAPIModelWindowReason, model+" · "+hit.Window)
	// Historical account locks are ignored by the shared read predicate. Never
	// clear persisted columns from this stale request snapshot: a concurrent
	// no-model 429 may already have established a genuine account-wide lock.
	return true
}

// NewAPIUsageWindowResetExtraKeys is shared by the Go and SQL legacy-lock
// predicates. New observations write the account marker with the snapshot.
func NewAPIUsageWindowResetExtraKeys() []string {
	return []string{newAPIWeeklyResetExtraKey, newAPIFiveHourResetExtraKey, newAPISevenDayResetExtraKey, newAPIMonthlyResetExtraKey}
}

func NewAPIAccountWindowLockIgnored(account *Account) bool {
	return newAPIAccountWindowLockIgnoredAt(account, time.Now())
}

func newAPIAccountWindowLockIgnoredAt(account *Account, now time.Time) bool {
	if account == nil || account.Platform != PlatformNewAPI || account.RateLimitResetAt == nil || !now.Before(*account.RateLimitResetAt) {
		return false
	}
	if parseExtraFloat64(account.Extra[NewAPIAccountWindowLockExtraKey]) > 0 {
		return false
	}
	reset := float64(account.RateLimitResetAt.Unix())
	for _, key := range NewAPIUsageWindowResetExtraKeys() {
		raw := parseExtraFloat64(account.Extra[key])
		if raw > 0 && math.Abs(raw-reset) < 2 {
			return true
		}
	}
	return false
}

func accountWideRateLimitBlocks(account *Account, now time.Time) bool {
	return account != nil && account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) && !newAPIAccountWindowLockIgnoredAt(account, now)
}

func (s *RateLimitService) persistNewAPIUsageWindowSnapshot(ctx context.Context, account *Account, hit *newAPIUsageWindowHit) bool {
	if s == nil || s.accountRepo == nil || account == nil || hit == nil {
		return false
	}
	utilKey, resetKey, sampledKey := newAPIUsageWindowExtraKeys(hit.Window)
	if utilKey == "" {
		return false
	}
	now := time.Now().UTC()
	updates := map[string]any{
		NewAPIAccountWindowLockExtraKey: 1.0,
		utilKey:                         1.0,
		resetKey:                        float64(hit.ResetAt.Unix()),
		sampledKey:                      now.Format(time.RFC3339Nano),
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("newapi_usage_window_extra_persist_failed",
			"account_id", account.ID, "window", hit.Window, "error", err)
		return false
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	for k, v := range updates {
		account.Extra[k] = v
	}
	return true
}

func newAPIUsageWindowExtraKeys(window string) (utilKey, resetKey, sampledKey string) {
	switch window {
	case "monthly":
		return newAPIMonthlyUtilExtraKey, newAPIMonthlyResetExtraKey, newAPIMonthlySampledExtraKey
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
	now := time.Now()
	apply := func(window, utilKey, resetKey, dimKey, dimLabel, dimWindow string, progress **UsageProgress) {
		if parseExtraFloat64(account.Extra[NewAPIAccountWindowLockExtraKey]) <= 0 {
			return
		}
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
		if progress != nil {
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
		}
		upsertNewAPIUsageWindowDimension(usage, UpstreamQuotaDimension{
			Key: dimKey, Label: dimLabel, Unit: "percent", Window: dimWindow,
			Utilization: &utilization, ResetsAt: resetAt,
		})
	}

	limits := account.ActiveModelRateLimits(now)
	models := make([]string, 0, len(limits))
	for model, limit := range limits {
		if newAPIModelUsageWindow(limit.Reason) != "" {
			models = append(models, model)
		}
	}
	sort.Strings(models)
	for _, model := range models {
		limit := limits[model]
		window := newAPIModelUsageWindow(limit.Reason)
		label := window
		switch window {
		case "monthly":
			label = "1mo"
		case "weekly":
			label = "7d"
		}
		reset, utilization := limit.RateLimitResetAt, 100.0
		upsertNewAPIUsageWindowDimension(usage, UpstreamQuotaDimension{
			Key: "newapi_" + window + ":" + model, Label: model, Unit: "percent", Window: label, Utilization: &utilization, ResetsAt: &reset,
		})
	}
	apply("monthly", newAPIMonthlyUtilExtraKey, newAPIMonthlyResetExtraKey, newAPIUpstreamMonthlyKey, "Monthly", "1mo", nil)
	apply("weekly", newAPIWeeklyUtilExtraKey, newAPIWeeklyResetExtraKey, newAPIUpstreamWeeklyKey, "Weekly", "7d", &usage.SevenDay)
	apply("5h", newAPIFiveHourUtilExtraKey, newAPIFiveHourResetExtraKey, newAPIUpstreamFiveHourKey, "5h", "5h", &usage.FiveHour)
	apply("7d", newAPISevenDayUtilExtraKey, newAPISevenDayResetExtraKey, newAPIUpstreamSevenDayKey, "7d", "7d", &usage.SevenDay)
}

func upsertNewAPIUsageWindowDimension(usage *UsageInfo, dimension UpstreamQuotaDimension) {
	if usage.UpstreamQuota == nil {
		usage.UpstreamQuota = baseUpstreamQuota(PlatformNewAPI, usage, "headers")
	}
	usage.UpstreamQuota.State = "degraded"
	usage.UpstreamQuota.ErrorCode = "rate_limited"
	usage.UpstreamQuota.StatusCode = 429
	for i := range usage.UpstreamQuota.Dimensions {
		if usage.UpstreamQuota.Dimensions[i].Key == dimension.Key {
			usage.UpstreamQuota.Dimensions[i] = dimension
			return
		}
	}
	usage.UpstreamQuota.Dimensions = append(usage.UpstreamQuota.Dimensions, dimension)
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

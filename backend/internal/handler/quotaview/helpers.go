// Package quotaview provides shared quota response helpers for user and admin handlers.
// Extracted to avoid import cycles between handler and handler/admin packages.
package quotaview

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// LazyZeroQuotaForResponse 按 D14 规则把过期档位归零（不写 DB）。
// includeWindowStart=true 时输出 *_window_start 字段（admin 视角调试用）
func LazyZeroQuotaForResponse(r service.UserPlatformQuotaRecord, now time.Time, includeWindowStart bool) map[string]any {
	daily := buildWindowSlice(r.DailyUsageUSD, r.DailyLimitUSD, r.DailyWindowStart, NeedsDailyReset(r.DailyWindowStart, now), nextDailyResetTime(now), includeWindowStart)
	weekly := buildWindowSlice(r.WeeklyUsageUSD, r.WeeklyLimitUSD, r.WeeklyWindowStart, NeedsWeeklyReset(r.WeeklyWindowStart, now), nextWeeklyResetTime(now), includeWindowStart)
	monthly := buildWindowSlice(r.MonthlyUsageUSD, r.MonthlyLimitUSD, r.MonthlyWindowStart, NeedsMonthlyReset(r.MonthlyWindowStart, now), NextMonthlyResetTimeFrom(r.MonthlyWindowStart, now), includeWindowStart)
	out := map[string]any{
		"platform":                 r.Platform,
		"daily_usage_usd":          daily.usage,
		"daily_limit_usd":          daily.limit,
		"daily_window_resets_at":   daily.resetsAt,
		"weekly_usage_usd":         weekly.usage,
		"weekly_limit_usd":         weekly.limit,
		"weekly_window_resets_at":  weekly.resetsAt,
		"monthly_usage_usd":        monthly.usage,
		"monthly_limit_usd":        monthly.limit,
		"monthly_window_resets_at": monthly.resetsAt,
	}
	if includeWindowStart {
		out["daily_window_start"] = daily.windowStart
		out["weekly_window_start"] = weekly.windowStart
		out["monthly_window_start"] = monthly.windowStart
	}
	return out
}

// PublicQuotaRecords returns the customer-facing quota view. Gemini and
// Antigravity are independent enforcement buckets internally, but users see
// one Google bucket whose usage/capacity is the sum of the two buckets.
// Admin handlers must continue using LazyZeroQuotaForResponse directly so
// operators can diagnose the individual source buckets.
func PublicQuotaRecords(records []service.UserPlatformQuotaRecord, now time.Time) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	index := make(map[string]int, len(records))
	for _, record := range records {
		view := LazyZeroQuotaForResponse(record, now, false)
		platform := publicPlatform(record.Platform)
		view["platform"] = platform
		if existingIndex, ok := index[platform]; ok {
			mergeQuotaView(out[existingIndex], view)
			continue
		}
		index[platform] = len(out)
		out = append(out, view)
	}
	return out
}

func publicPlatform(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "gemini", "antigravity", "google":
		return "google"
	default:
		return platform
	}
}

func mergeQuotaView(dst, src map[string]any) {
	for _, field := range []string{
		"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd",
	} {
		dst[field] = numberValue(dst[field]) + numberValue(src[field])
	}
	for _, field := range []string{
		"daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd",
	} {
		// A nil limit means unlimited. The aggregate is unlimited whenever
		// either internal source is unlimited; otherwise capacities add.
		dstLimit, dstHasLimit := quotaNumber(dst[field])
		srcLimit, srcHasLimit := quotaNumber(src[field])
		if !dstHasLimit || !srcHasLimit {
			dst[field] = nil
			continue
		}
		dst[field] = dstLimit + srcLimit
	}
	for _, field := range []string{
		"daily_window_resets_at", "weekly_window_resets_at", "monthly_window_resets_at",
	} {
		// A single reset timestamp is only meaningful when both buckets share
		// the same window. Keep it for that common case; otherwise hide it.
		dstReset, dstHasReset := quotaString(dst[field])
		srcReset, srcHasReset := quotaString(src[field])
		if !dstHasReset || !srcHasReset || dstReset != srcReset {
			dst[field] = nil
		}
	}
}

func numberValue(value any) float64 {
	if n, ok := value.(float64); ok {
		return n
	}
	if n, ok := value.(*float64); ok && n != nil {
		return *n
	}
	return 0
}

func quotaNumber(value any) (float64, bool) {
	if value == nil {
		return 0, false
	}
	if n, ok := value.(float64); ok {
		return n, true
	}
	if n, ok := value.(*float64); ok && n != nil {
		return *n, true
	}
	return 0, false
}

func quotaString(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	if s, ok := value.(string); ok {
		return s, true
	}
	if s, ok := value.(*string); ok && s != nil {
		return *s, true
	}
	return "", false
}

type windowSlice struct {
	usage       float64
	limit       *float64
	resetsAt    *string
	windowStart *string
}

func buildWindowSlice(usage float64, limit *float64, start *time.Time, expired bool, nextReset time.Time, includeStart bool) windowSlice {
	out := windowSlice{usage: usage, limit: limit}
	if expired {
		out.usage = 0
		out.resetsAt = nil
	} else if start != nil {
		s := nextReset.Format(time.RFC3339)
		out.resetsAt = &s
	}
	if includeStart && start != nil {
		s := start.Format(time.RFC3339)
		out.windowStart = &s
	}
	return out
}

// NeedsDailyReset 判断日窗口是否已过期：start 早于「全局时区当天 0 点」即过期。
// 时区跟随 timezone.Location()（全局服务器时区），与 billing / repo 写入的 window_start 同口径。
func NeedsDailyReset(start *time.Time, now time.Time) bool {
	if start == nil {
		return false
	}
	return start.Before(timezone.StartOfDay(now))
}

func NeedsWeeklyReset(start *time.Time, now time.Time) bool {
	if start == nil {
		return false
	}
	return start.Before(timezone.StartOfWeek(now))
}

// NeedsMonthlyReset 30 天滚动窗口语义（与订阅模式 NeedsMonthlyReset 一致）。
func NeedsMonthlyReset(start *time.Time, now time.Time) bool {
	if start == nil {
		return false
	}
	return now.Sub(*start) >= 30*24*time.Hour
}

func nextDailyResetTime(now time.Time) time.Time {
	return timezone.StartOfDay(now).AddDate(0, 0, 1)
}

func nextWeeklyResetTime(now time.Time) time.Time {
	return timezone.StartOfWeek(now).AddDate(0, 0, 7)
}

// NextMonthlyResetTimeFrom 计算 30 天滚动月度窗口的下次重置时间。
// 语义：
//   - start != nil → 返回 start + 30d（与 billing_cache_service.nextMonthlyResetFrom 一致）
//   - start == nil → 退化为 now + 30d（保留旧行为，避免 nil 崩溃）
//
// 导出（首字母大写）以允许测试直接调用。
func NextMonthlyResetTimeFrom(start *time.Time, now time.Time) time.Time {
	if start == nil {
		return now.Add(30 * 24 * time.Hour)
	}
	return start.Add(30 * 24 * time.Hour)
}

package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/gin-gonic/gin"
)

// TK: absolute-time + canonical-calendar window channels for the admin dashboard.
//
// The default `start_date`/`end_date` channel is date-granular and re-expanded
// by parseTimeRange into the *user's timezone* day boundaries. That makes a
// rolling preset like "近24小时" resolve to a different absolute window per
// viewer timezone — two admins (e.g. one browser reporting Asia/Shanghai, one
// reporting a US zone) then see materially different numbers for the same
// selection. Rolling/duration presets are an absolute concept ("the last N
// hours" is the same window everywhere on Earth), so the frontend sends precise
// epoch-millisecond instants for them and we honor those verbatim, bypassing
// the timezone-dependent date expansion entirely.
//
// Calendar presets (today / yesterday / this month / last month) are a
// *billing-day* concept in this system: quota resets, the customer-facing
// dashboard ("今日消费", via timezone.Today()), and the rollup buckets
// (usage_dashboard_user_platform_daily.bucket_date) are ALL anchored to the
// server/reporting timezone. Resolving these presets against the *viewer's*
// browser timezone instead (the legacy date-string + auto-attached `timezone`
// path) made the admin per-user "今天" window refer to a different calendar day
// than the customer's own dashboard whenever the operator's browser timezone
// differed from the server — the two surfaces then reported different totals for
// the same user/day. To keep a single canonical definition of "today", the
// frontend now sends a named `range` for these presets and we resolve it here in
// the SERVER timezone via the timezone package — byte-identical to what the
// customer dashboard's timezone.Today() produces. Custom manual date picks (no
// `range`) still flow through the legacy date-string path.

// parseNamedRange resolves a calendar preset (`range` query param) into an
// absolute [start, end) window anchored to the server/reporting timezone, so it
// matches the customer dashboard and the daily rollup buckets exactly. It
// returns ok=false for an absent/unknown value, leaving existing behavior
// untouched. The viewer's `timezone` query param is intentionally ignored here:
// "今天" must mean the same billing day for every viewer.
func parseNamedRange(c *gin.Context) (start time.Time, end time.Time, ok bool) {
	switch strings.TrimSpace(c.Query("range")) {
	case "today":
		s := timezone.Today()
		return s, s.AddDate(0, 0, 1), true
	case "yesterday":
		e := timezone.Today()
		return e.AddDate(0, 0, -1), e, true
	case "this_month":
		s := timezone.StartOfMonth(timezone.Now())
		return s, s.AddDate(0, 1, 0), true
	case "last_month":
		thisMonth := timezone.StartOfMonth(timezone.Now())
		return thisMonth.AddDate(0, -1, 0), thisMonth, true
	default:
		return time.Time{}, time.Time{}, false
	}
}

// absoluteRangeFloor guards against absurd / malicious timestamps. Any data
// predates this; anything before it is treated as "not an absolute range".
var absoluteRangeFloor = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// parseAbsoluteRange reads start_ts / end_ts (epoch milliseconds) from the
// request. It returns ok=true only when BOTH are present, parse cleanly, sit
// after absoluteRangeFloor, are not unreasonably far in the future, and form a
// non-empty window (end > start). When ok=false the caller falls back to the
// legacy date-string + timezone path, so the absence or malformation of these
// params never changes existing behavior.
func parseAbsoluteRange(c *gin.Context) (start time.Time, end time.Time, ok bool) {
	startMs, ok1 := parseEpochMillis(c.Query("start_ts"))
	endMs, ok2 := parseEpochMillis(c.Query("end_ts"))
	if !ok1 || !ok2 {
		return time.Time{}, time.Time{}, false
	}

	start = time.UnixMilli(startMs).UTC()
	end = time.UnixMilli(endMs).UTC()

	if !end.After(start) {
		return time.Time{}, time.Time{}, false
	}
	if start.Before(absoluteRangeFloor) {
		return time.Time{}, time.Time{}, false
	}
	// Allow a little clock skew but reject windows that run far into the future.
	if end.After(time.Now().UTC().Add(2 * time.Hour)) {
		return time.Time{}, time.Time{}, false
	}

	return start, end, true
}

func parseEpochMillis(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

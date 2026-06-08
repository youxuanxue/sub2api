package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// TK: absolute-time window channel for the admin dashboard.
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
// Calendar presets (today / this month / custom date pick) keep using the
// date-string + timezone path, where per-viewer local-calendar boundaries are
// the intended behavior.

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

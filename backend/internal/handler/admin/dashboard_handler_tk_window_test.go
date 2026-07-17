package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func ctxWithURL(rawURL string) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, rawURL, nil)
	return c
}

// TestParseAbsoluteRange_TimezoneIndependent is the regression guard for the
// "two machines see different dashboard data" bug: given precise start_ts/end_ts,
// the resolved window must be identical regardless of the timezone query param.
func TestParseAbsoluteRange_TimezoneIndependent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	endMs := time.Date(2026, 6, 8, 15, 8, 0, 0, time.UTC).UnixMilli()
	startMs := endMs - 24*60*60*1000

	shanghai := ctxWithURL(fmt.Sprintf("/?start_ts=%d&end_ts=%d&timezone=Asia/Shanghai", startMs, endMs))
	la := ctxWithURL(fmt.Sprintf("/?start_ts=%d&end_ts=%d&timezone=America/Los_Angeles", startMs, endMs))

	s1, e1 := parseTimeRange(shanghai)
	s2, e2 := parseTimeRange(la)

	require.Equal(t, s1, s2, "start must not depend on timezone when absolute ts given")
	require.Equal(t, e1, e2, "end must not depend on timezone when absolute ts given")
	require.Equal(t, time.UnixMilli(startMs).UTC(), s1)
	require.Equal(t, time.UnixMilli(endMs).UTC(), e1)
	require.Equal(t, 24*time.Hour, e1.Sub(s1))
}

func TestParseAbsoluteRange_RejectsAndFallsBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC().UnixMilli()

	cases := []struct {
		name string
		url  string
	}{
		{"missing end_ts", fmt.Sprintf("/?start_ts=%d", now)},
		{"missing start_ts", fmt.Sprintf("/?end_ts=%d", now)},
		{"empty", "/"},
		{"non-numeric", "/?start_ts=abc&end_ts=def"},
		{"end before start", fmt.Sprintf("/?start_ts=%d&end_ts=%d", now, now-1000)},
		{"end equals start", fmt.Sprintf("/?start_ts=%d&end_ts=%d", now, now)},
		{"before floor", fmt.Sprintf("/?start_ts=%d&end_ts=%d", int64(1000), now)},
		{"far future", fmt.Sprintf("/?start_ts=%d&end_ts=%d", now, now+24*60*60*1000)},
		{"negative", "/?start_ts=-5&end_ts=-1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok := parseAbsoluteRange(ctxWithURL(tc.url))
			require.False(t, ok, "expected absolute-range parse to reject %q", tc.url)
		})
	}
}

// TestParseTimeRange_DateStringUsesServerTZ ensures manual start_date/end_date
// parsing ignores the viewer timezone query param and uses server TZ instead.
func TestParseTimeRange_DateStringUsesServerTZ(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))
	t.Cleanup(func() { _ = timezone.Init("UTC") })

	gin.SetMode(gin.TestMode)
	c := ctxWithURL("/?start_date=2024-01-01&end_date=2024-01-02&timezone=Asia/Shanghai")
	loc := timezone.Location()
	start, end := parseTimeRange(c)
	require.Equal(t, time.Date(2024, 1, 1, 0, 0, 0, 0, loc), start)
	require.Equal(t, time.Date(2024, 1, 3, 0, 0, 0, 0, loc), end)
}

// TestParseNamedRange_ServerTZCanonical is the regression guard for the
// "admin per-user 今日消费 ≠ customer 今日消费" bug: a calendar `range` preset must
// resolve to the SERVER/billing timezone (matching the customer dashboard's
// timezone.Today()), ignoring both the viewer `timezone` param and any
// browser-local start_date/end_date that rides along.
func TestParseNamedRange_ServerTZCanonical(t *testing.T) {
	gin.SetMode(gin.TestMode)

	today := timezone.Today()
	thisMonth := timezone.StartOfMonth(timezone.Now())

	cases := []struct {
		name      string
		rng       string
		wantStart time.Time
		wantEnd   time.Time
	}{
		{"today", "today", today, today.AddDate(0, 0, 1)},
		{"yesterday", "yesterday", today.AddDate(0, 0, -1), today},
		{"this_month", "this_month", thisMonth, thisMonth.AddDate(0, 1, 0)},
		{"last_month", "last_month", thisMonth.AddDate(0, -1, 0), thisMonth},
	}

	// Every viewer timezone (and a bogus browser-local date) must yield the same
	// canonical server-TZ window.
	for _, tz := range []string{"Asia/Shanghai", "America/Los_Angeles", "UTC"} {
		for _, tc := range cases {
			t.Run(tc.name+"/"+tz, func(t *testing.T) {
				url := fmt.Sprintf("/?range=%s&start_date=1999-01-01&end_date=1999-01-01&timezone=%s", tc.rng, tz)
				start, end := parseTimeRange(ctxWithURL(url))
				require.Equal(t, tc.wantStart, start)
				require.Equal(t, tc.wantEnd, end)
			})
		}
	}
}

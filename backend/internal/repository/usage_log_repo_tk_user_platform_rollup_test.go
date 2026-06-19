//go:build unit

package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// TestPlanUsageRollupWindow verifies the [start,end) -> rollup + raw remainder
// decomposition that keeps the rollup-backed reads byte-exact. The key
// invariants: (1) the rollup span only ever covers FULL completed server-TZ days
// strictly before today; (2) the raw remainders exactly cover the rest of the
// window (partial head day + partial tail/today) with no gap and no overlap.
func TestPlanUsageRollupWindow(t *testing.T) {
	today := timezone.Today()
	day := 24 * time.Hour

	t.Run("default 30d window splits into rollup middle + partial head + today tail", func(t *testing.T) {
		// startTime is mid-day 30 days ago (the GetBatchUserUsageStats default).
		start := today.Add(-30 * day).Add(9 * time.Hour)
		end := today.Add(15 * time.Hour) // some point during today
		w := planUsageRollupWindow(start, end)

		require.True(t, w.hasRollup)
		// First full day is the day after the partial start day.
		require.Equal(t, today.Add(-29*day), w.rollupStartDay)
		// Rollup ends at start-of-today (today's bucket is excluded).
		require.Equal(t, today, w.rollupEndDay)
		// Two raw remainders: partial head [start, ceilDay(start)) and tail [today, end).
		require.Len(t, w.rawSpans, 2)
		require.Equal(t, start, w.rawSpans[0][0])
		require.Equal(t, today.Add(-29*day), w.rawSpans[0][1])
		require.Equal(t, today, w.rawSpans[1][0])
		require.Equal(t, end, w.rawSpans[1][1])
		assertWindowCoverage(t, start, end, w)
	})

	t.Run("day-aligned window has no partial head", func(t *testing.T) {
		start := today.Add(-7 * day) // exactly midnight 7 days ago
		end := today                 // exactly start of today
		w := planUsageRollupWindow(start, end)

		require.True(t, w.hasRollup)
		require.Equal(t, start, w.rollupStartDay)
		require.Equal(t, today, w.rollupEndDay)
		// No raw remainder: the window is exactly 7 completed days.
		require.Empty(t, w.rawSpans)
		assertWindowCoverage(t, start, end, w)
	})

	t.Run("today-only window is fully raw", func(t *testing.T) {
		start := today
		end := today.Add(10 * time.Hour)
		w := planUsageRollupWindow(start, end)

		require.False(t, w.hasRollup)
		require.Len(t, w.rawSpans, 1)
		require.Equal(t, start, w.rawSpans[0][0])
		require.Equal(t, end, w.rawSpans[0][1])
		assertWindowCoverage(t, start, end, w)
	})

	t.Run("sub-day window inside today is fully raw", func(t *testing.T) {
		start := today.Add(2 * time.Hour)
		end := today.Add(5 * time.Hour)
		w := planUsageRollupWindow(start, end)

		require.False(t, w.hasRollup)
		require.Len(t, w.rawSpans, 1)
		assertWindowCoverage(t, start, end, w)
	})

	t.Run("purely historical window (ends before today) uses rollup, no today tail", func(t *testing.T) {
		start := today.Add(-10 * day)
		end := today.Add(-3 * day) // day-aligned, all completed
		w := planUsageRollupWindow(start, end)

		require.True(t, w.hasRollup)
		require.Equal(t, start, w.rollupStartDay)
		require.Equal(t, end, w.rollupEndDay)
		require.Empty(t, w.rawSpans)
		assertWindowCoverage(t, start, end, w)
	})

	t.Run("historical window with partial tail day reads tail from raw", func(t *testing.T) {
		start := today.Add(-10 * day)
		end := today.Add(-3 * day).Add(6 * time.Hour) // partial last day
		w := planUsageRollupWindow(start, end)

		require.True(t, w.hasRollup)
		require.Equal(t, start, w.rollupStartDay)
		require.Equal(t, today.Add(-3*day), w.rollupEndDay)
		require.Len(t, w.rawSpans, 1)
		require.Equal(t, today.Add(-3*day), w.rawSpans[0][0])
		require.Equal(t, end, w.rawSpans[0][1])
		assertWindowCoverage(t, start, end, w)
	})

	t.Run("window shorter than one full day but spanning midnight is fully raw", func(t *testing.T) {
		// start yesterday 23:00, end today 01:00 -> no FULL completed day fits.
		start := today.Add(-1 * time.Hour)
		end := today.Add(1 * time.Hour)
		w := planUsageRollupWindow(start, end)

		require.False(t, w.hasRollup)
		require.Len(t, w.rawSpans, 1)
		assertWindowCoverage(t, start, end, w)
	})
}

// assertWindowCoverage proves the decomposition partitions [start,end) with no
// gap and no overlap: concatenating the rollup span (if any) and the raw spans,
// sorted, must reconstruct exactly [start,end).
func assertWindowCoverage(t *testing.T, start, end time.Time, w usageRollupWindow) {
	t.Helper()
	type seg struct{ from, to time.Time }
	segs := make([]seg, 0, 3)
	if w.hasRollup {
		segs = append(segs, seg{w.rollupStartDay, w.rollupEndDay})
	}
	for _, s := range w.rawSpans {
		segs = append(segs, seg{s[0], s[1]})
	}
	// sort by from
	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			if segs[j].from.Before(segs[i].from) {
				segs[i], segs[j] = segs[j], segs[i]
			}
		}
	}
	require.NotEmpty(t, segs, "window must be covered")
	require.True(t, segs[0].from.Equal(start), "coverage must start at window start")
	for i := 1; i < len(segs); i++ {
		require.True(t, segs[i-1].to.Equal(segs[i].from), "segments must be contiguous (no gap/overlap)")
	}
	require.True(t, segs[len(segs)-1].to.Equal(end), "coverage must end at window end")
}

//go:build unit

package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountWindowStatsSQLCollapsesPageQueries(t *testing.T) {
	oneStart := strings.Join(strings.Fields(accountWindowStatsForStartsQuery(1)), " ")
	require.Contains(t, oneStart, "account_id = ANY($1)")
	require.Contains(t, oneStart, "created_at >= $2")
	require.NotContains(t, oneStart, "UNION ALL")
	require.NotContains(t, oneStart, "unnest")

	twoStarts := strings.Join(strings.Fields(accountWindowStatsForStartsQuery(2)), " ")
	require.Contains(t, twoStarts, "UNION ALL")
	require.Contains(t, twoStarts, "account_id = ANY($1)")
	require.Contains(t, twoStarts, "created_at >= $2")
	require.Contains(t, twoStarts, "account_id = ANY($3)")
	require.Contains(t, twoStarts, "created_at >= $4")
	require.NotContains(t, twoStarts, "unnest")

	dual := strings.Join(strings.Fields(accountWindowStatsDualWindowSQL), " ")
	require.Contains(t, dual, "created_at >= $2")
	require.Contains(t, dual, "FILTER (WHERE created_at >= $3)")
	require.Contains(t, dual, "GROUP BY account_id")
	require.NotContains(t, dual, "unnest")
}

func TestGroupAccountWindowStartsCollapsesEqualInstants(t *testing.T) {
	start := time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC)
	sameInstant := start.In(time.FixedZone("CST", 8*3600))
	later := start.Add(time.Hour)

	groups := groupAccountWindowStarts(
		[]int64{1, 2, 3},
		[]time.Time{start, sameInstant, later},
	)

	require.Len(t, groups, 2)
	require.Equal(t, []int64{1, 2}, groups[0].ids)
	require.True(t, groups[0].start.Equal(start))
	require.Equal(t, []int64{3}, groups[1].ids)
	require.True(t, groups[1].start.Equal(later))
}

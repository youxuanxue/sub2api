//go:build unit

package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountWindowStatsSQLCollapsesPageQueries(t *testing.T) {
	forStarts := strings.Join(strings.Fields(accountWindowStatsForStartsSQL), " ")
	require.Contains(t, forStarts, "unnest($1::bigint[], $2::timestamptz[])")
	require.Contains(t, forStarts, "LEFT JOIN usage_logs u")
	require.Contains(t, forStarts, "u.created_at >= w.start_at")
	require.Contains(t, forStarts, "COUNT(u.account_id)")

	dual := strings.Join(strings.Fields(accountWindowStatsDualWindowSQL), " ")
	require.Contains(t, dual, "created_at >= $2")
	require.Contains(t, dual, "FILTER (WHERE created_at >= $3)")
	require.Contains(t, dual, "GROUP BY account_id")
	require.NotContains(t, dual, "unnest")
}

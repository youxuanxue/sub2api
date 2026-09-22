package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountWideRateLimitNotBlockingSQL_IncludesModelCountCascade(t *testing.T) {
	sql := accountWideRateLimitNotBlockingSQL("a")
	require.Contains(t, sql, "model_rate_limits")
	require.Contains(t, sql, "AICredits")
	require.Contains(t, sql, "jsonb_each")
	require.Contains(t, sql, service.PlatformNewAPI)
	require.Contains(t, sql, "<= 3")
	require.True(t, strings.Contains(sql, "a.rate_limit_reset_at") || strings.Contains(sql, "rate_limit_reset_at"))

	countSQL := activeModelRateLimitCountNotOverflowingSQL("a.extra", "NOW()", time.Time{})
	require.Contains(t, countSQL, "NOW()")
	require.Contains(t, countSQL, "<= 3")
}

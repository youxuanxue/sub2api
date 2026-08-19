package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTK087DisablesUpstreamErrorRateP0WithoutDeleting(t *testing.T) {
	content, err := FS.ReadFile("tk_087_disable_upstream_error_rate_p0.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "name = '上游错误率极高'")
	require.Contains(t, sql, "metric_type = 'upstream_error_rate'")
	require.Contains(t, sql, "enabled = false")
	require.Contains(t, sql, "AND enabled = true")
	require.Contains(t, sql, "status = 'resolved'")
	require.Contains(t, sql, "name = '上游错误率偏高'")
	require.NotContains(t, sql, "DELETE FROM ops_alert_rules")
	require.Contains(t, sql, "user_visible_failure_count")
}

package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTK087RemovesElevatedUpstreamRateAndKeepsEdgeP1(t *testing.T) {
	content, err := FS.ReadFile("tk_087_disable_upstream_error_rate_p0.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DELETE FROM ops_alert_rules")
	require.Contains(t, sql, "name = '上游错误率偏高'")
	require.Contains(t, sql, "name = '上游错误率极高'")
	require.Contains(t, sql, "metric_type = 'upstream_error_rate'")
	require.Contains(t, sql, "severity = 'P1'")
	require.Contains(t, sql, "enabled = true")
	require.Contains(t, sql, "user_visible_failure_count")
	require.NotContains(t, sql, "enabled = false")
}

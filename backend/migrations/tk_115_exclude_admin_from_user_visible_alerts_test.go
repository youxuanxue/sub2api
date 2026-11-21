package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTK115DocumentsAdminExclusionInUserVisibleAlertDescriptions(t *testing.T) {
	content, err := FS.ReadFile("tk_115_exclude_admin_from_user_visible_alerts.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "name = '真实用户体验受损'")
	require.Contains(t, sql, "metric_type = 'user_visible_failure_count'")
	require.Contains(t, sql, "name = '真实用户客户端失败增多'")
	require.Contains(t, sql, "metric_type = 'client_visible_failure_count'")
	require.Contains(t, sql, "users.role=admin")
	require.NotContains(t, sql, "threshold =")
	require.NotContains(t, sql, "cooldown_minutes")
}

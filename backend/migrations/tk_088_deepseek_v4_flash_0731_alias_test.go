package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTK088TargetsOnlyTheVolcengineAgentPlanAccount(t *testing.T) {
	content, err := FS.ReadFile("tk_088_deepseek_v4_flash_0731_alias.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, guard := range []string{
		"id = 88",
		"name = 'volcengine-agent-plan'",
		"platform = 'newapi'",
		"channel_type = 45",
		"deleted_at IS NULL",
	} {
		require.Contains(t, sql, guard)
	}
	require.NotContains(t, sql, "id = 39")
	require.Contains(t, sql, `"deepseek-v4-flash-0731": "deepseek-v4-flash"`)
}

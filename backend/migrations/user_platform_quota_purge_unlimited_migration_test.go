package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUserPlatformQuotasPurgeUnlimitedMigration 校验 238 号迁移只删除三档限额全为 NULL 的行：
// 这类行等价于"不存在"，任何一档非 NULL 的记录（含软删历史）都必须保留。
// 超时守卫防止大表 DELETE 在 Stage0 升级窗口无限挂起。
func TestUserPlatformQuotasPurgeUnlimitedMigration(t *testing.T) {
	content, err := FS.ReadFile("238_purge_unlimited_user_platform_quotas.sql")
	require.NoError(t, err)

	var stmts []string
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		stmts = append(stmts, trimmed)
	}
	sql := strings.Join(stmts, " ")
	require.Equal(t,
		"SET LOCAL lock_timeout = '5s'; SET LOCAL statement_timeout = '10min'; DELETE FROM user_platform_quotas WHERE daily_limit_usd IS NULL AND weekly_limit_usd IS NULL AND monthly_limit_usd IS NULL;",
		sql,
	)
}

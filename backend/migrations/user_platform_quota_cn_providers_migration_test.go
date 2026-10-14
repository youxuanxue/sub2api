package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUserPlatformQuotasCNProvidersMigration 校验 224 号迁移把 kimi/zhipu/deepseek
// 加入 user_platform_quotas.platform 的 CHECK 约束（对照 157 号 grok 迁移）。
// 约束未放宽时，注册预填充 8 平台默认配额会整条 INSERT 中止 → 新用户零配额行
// （缺失配额行 = 无限额），管理端设置国产平台配额直接 500。
func TestUserPlatformQuotasCNProvidersMigration(t *testing.T) {
	content, err := FS.ReadFile("224_user_platform_quotas_add_cn_providers.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check")
	require.Contains(t, sql,
		"CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek'))")
}

// upstream 242 彻底移除 platform CHECK 约束（平台成员资格由应用层目录校验）。
// 已发布的 tk_083/tk_087/tk_096 不可变（仍会重建放宽版 CHECK，文件名排序在 242 之后），
// 因此由 tk_103 在其后幂等 DROP，使全新安装与已迁移环境都收敛到 post-242 形态。
func TestUserPlatformQuotasTK103DropsPlatformCheckAfterWideningMigrations(t *testing.T) {
	content, err := FS.ReadFile("tk_103_user_platform_quotas_drop_platform_check.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check")
	require.NotContains(t, sql, "ADD CONSTRAINT")

	// 迁移按文件名字典序执行：tk_103 必须排在所有重建 CHECK 的迁移之后。
	for _, earlier := range []string{
		"242_drop_platform_check_constraints.sql",
		"tk_083_user_platform_quotas_allow_newapi_kiro.sql",
		"tk_087_user_platform_quotas_allow_all_served_platforms.sql",
		"tk_096_quota_platforms_include_minimax.sql",
	} {
		require.Less(t, earlier, "tk_103_user_platform_quotas_drop_platform_check.sql",
			"%s must sort before tk_103", earlier)
	}
}

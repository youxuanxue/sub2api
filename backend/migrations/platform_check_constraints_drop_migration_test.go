package migrations

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDropPlatformCheckConstraintsMigration(t *testing.T) {
	content, err := FS.ReadFile("242_drop_platform_check_constraints.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql,
		"ALTER TABLE user_platform_quotas DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;")
	require.Contains(t, sql,
		"ALTER TABLE composite_model_routes DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;")
	require.NotContains(t, sql, "ADD CONSTRAINT")
	require.NotContains(t, sql, "DROP CONSTRAINT IF EXISTS channel_monitors_provider_check")
	require.NotContains(t, sql, "DROP CONSTRAINT IF EXISTS channel_monitor_request_templates_provider_check")
}

// Platform membership is validated against the application catalog after 242.
// A later migration must not restore a database whitelist that can drift from it.
func TestLaterMigrationsDoNotRestorePlatformCheckConstraints(t *testing.T) {
	const droppedAt = "242_drop_platform_check_constraints.sql"
	// TK legacy: these three shipped migrations are immutable (preflight migration
	// immutability gate) and historically re-ADD a widened CHECK after 242 in
	// filename order. tk_103_user_platform_quotas_drop_platform_check.sql drops the
	// constraint again behind them, so the final state still has no DB whitelist.
	// Any NEW migration re-adding the constraint remains forbidden.
	tkLegacyRepairedByTK103 := map[string]bool{
		"tk_083_user_platform_quotas_allow_newapi_kiro.sql":          true,
		"tk_087_user_platform_quotas_allow_all_served_platforms.sql": true,
		"tk_096_quota_platforms_include_minimax.sql":                 true,
	}
	entries, err := fs.ReadDir(FS, ".")
	require.NoError(t, err)
	comments := regexp.MustCompile(`(?ms)/\*.*?\*/|--[^\n]*`)
	addConstraint := regexp.MustCompile(`(?i)\bADD\s+CONSTRAINT\s+"?(user_platform_quotas_platform_check|composite_model_routes_target_platform_check)\b`)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() <= droppedAt {
			continue
		}
		if tkLegacyRepairedByTK103[entry.Name()] {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			content, err := FS.ReadFile(entry.Name())
			require.NoError(t, err)
			sql := comments.ReplaceAll(content, nil)
			require.Nil(t, addConstraint.Find(sql), "platform CHECK constraints removed in 242 must remain managed by application validation")
		})
	}
}

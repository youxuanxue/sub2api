//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApplyMigrations_Upstream224DoesNotBreakLiveNewAPIKiroRows reproduces the
// 1.8.166 us4 canary failure: tk_083 already stored newapi/kiro quota rows, then
// upstream 224 tried to install a narrower CHECK and aborted startup.
func TestApplyMigrations_Upstream224DoesNotBreakLiveNewAPIKiroRows(t *testing.T) {
	ctx := context.Background()
	db := openMigrationIntegrationDatabase(t, "migration_224_live_newapi_kiro")

	_, err := db.ExecContext(ctx, `
		CREATE TABLE user_platform_quotas (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL,
			platform VARCHAR(32) NOT NULL,
			daily_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0,
			weekly_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0,
			monthly_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		ALTER TABLE user_platform_quotas
			ADD CONSTRAINT user_platform_quotas_platform_check
			CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'newapi', 'kiro', 'grok'));
		INSERT INTO user_platform_quotas (user_id, platform) VALUES (1, 'newapi'), (1, 'kiro');
	`)
	require.NoError(t, err)

	t.Run("upstream 224 SQL is unsafe on this fixture", func(t *testing.T) {
		tx, txErr := db.BeginTx(ctx, nil)
		require.NoError(t, txErr)
		t.Cleanup(func() { _ = tx.Rollback() })

		content := string(migrationFilesFS(t, userPlatformQuotasCNProvidersMigration)[userPlatformQuotasCNProvidersMigration].Data)
		_, execErr := tx.ExecContext(ctx, content)
		require.Error(t, execErr)
		require.Contains(t, execErr.Error(), "user_platform_quotas_platform_check")
	})

	require.NoError(t, applyMigrationsFS(ctx, db, migrationFilesFS(t,
		userPlatformQuotasCNProvidersMigration,
		"tk_087_user_platform_quotas_allow_all_served_platforms.sql",
	)))

	assertMigrationsRecorded(t, db,
		userPlatformQuotasCNProvidersMigration,
		"tk_087_user_platform_quotas_allow_all_served_platforms.sql",
	)

	var constraintDef string
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conname = 'user_platform_quotas_platform_check'
	`).Scan(&constraintDef))
	for _, platform := range []string{"newapi", "kiro", "kimi", "zhipu", "deepseek"} {
		require.Contains(t, constraintDef, platform, "canonical CHECK must keep %s", platform)
	}

	var liveRows int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM user_platform_quotas WHERE platform IN ('newapi', 'kiro')
	`).Scan(&liveRows))
	require.Equal(t, 2, liveRows, "live newapi/kiro quota rows must survive the 224/tk_087 sequence")
}

//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

var upstream20261013Migrations = []string{
	"242_drop_platform_check_constraints.sql",
	"242_group_i18n.sql",
	"243_gemini_cached_contents.sql",
	"244_gemini_cached_contents_channel_usage.sql",
	"245_gemini_cached_contents_mutation_lease.sql",
	"tk_103_user_platform_quotas_drop_platform_check.sql",
}

func createUpstream20261013Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		CREATE TABLE groups (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL);
		INSERT INTO groups (name) VALUES ('existing');
		CREATE TABLE user_platform_quotas (
			user_id BIGINT, platform TEXT, monthly_usage_usd NUMERIC NOT NULL DEFAULT 0,
			CONSTRAINT user_platform_quotas_platform_check CHECK (platform IN ('newapi','kiro'))
		);
		INSERT INTO user_platform_quotas VALUES (1,'newapi',12.5),(1,'kiro',3.25);
		CREATE TABLE composite_model_routes (
			target_platform TEXT,
			CONSTRAINT composite_model_routes_target_platform_check CHECK (target_platform IN ('openai'))
		);
		INSERT INTO composite_model_routes VALUES ('openai');
	`)
	require.NoError(t, err)
}

func TestUpstream20261013Migrations_PreserveOldWriterAndRetry(t *testing.T) {
	ctx := context.Background()
	db := openMigrationIntegrationDatabase(t, "upstream_20261013_rolling")
	createUpstream20261013Fixture(t, db)
	fsys := migrationFilesFS(t, upstream20261013Migrations...)
	require.NoError(t, applyMigrationsFS(ctx, db, fsys))
	require.NoError(t, applyMigrationsFS(ctx, db, fsys))
	assertMigrationsRecorded(t, db, upstream20261013Migrations...)

	// The old image writes only its known columns while the new image is warming.
	_, err := db.ExecContext(ctx, `INSERT INTO groups (name) VALUES ('old-writer');
		UPDATE user_platform_quotas SET monthly_usage_usd=monthly_usage_usd+1 WHERE platform='newapi'`)
	require.NoError(t, err)
	var groups int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM groups WHERE i18n='{}'::jsonb`).Scan(&groups))
	require.Equal(t, 2, groups)
	var usage float64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT sum(monthly_usage_usd) FROM user_platform_quotas`).Scan(&usage))
	require.Equal(t, 16.75, usage)
	var constraints int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_constraint WHERE conname IN
		('user_platform_quotas_platform_check','composite_model_routes_target_platform_check')`).Scan(&constraints))
	require.Zero(t, constraints)
	var lockTimeout, statementTimeout string
	require.NoError(t, db.QueryRowContext(ctx, `SHOW lock_timeout`).Scan(&lockTimeout))
	require.NoError(t, db.QueryRowContext(ctx, `SHOW statement_timeout`).Scan(&statementTimeout))
	require.Equal(t, "0", lockTimeout, "migration budgets must not leak into application sessions")
	require.Equal(t, "0", statementTimeout)
}

func TestUpstream20261013Migrations_LiveReaderBoundsDDLWait(t *testing.T) {
	for _, tt := range []struct{ name, table string }{
		{"242_group_i18n.sql", "groups"},
		{"242_drop_platform_check_constraints.sql", "composite_model_routes"},
		{"tk_103_user_platform_quotas_drop_platform_check.sql", "user_platform_quotas"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := openMigrationIntegrationDatabase(t, "upstream_20261013_locked")
			db.SetMaxOpenConns(2)
			createUpstream20261013Fixture(t, db)
			holder, err := db.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			defer func() { _ = holder.Rollback() }()
			_, err = holder.ExecContext(context.Background(), "SELECT 1 FROM "+pq.QuoteIdentifier(tt.table))
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			err = applyMigrationsFS(ctx, db, migrationFilesFS(t, tt.name))
			var pgErr *pq.Error
			require.ErrorAs(t, err, &pgErr)
			require.Equal(t, pq.ErrorCode("55P03"), pgErr.Code, "DDL must fail on lock_timeout before the caller deadline")
			var recorded int
			require.NoError(t, db.QueryRowContext(context.Background(), `SELECT count(*) FROM schema_migrations WHERE filename=$1`, tt.name).Scan(&recorded))
			require.Zero(t, recorded, "a failed migration must remain retryable")
			var constraints int
			require.NoError(t, db.QueryRowContext(context.Background(), `SELECT count(*) FROM pg_constraint WHERE conname IN
				('user_platform_quotas_platform_check','composite_model_routes_target_platform_check')`).Scan(&constraints))
			require.Equal(t, 2, constraints, "even a failure on the second table must roll back the first ALTER")
			require.NoError(t, holder.Rollback())
			require.NoError(t, applyMigrationsFS(context.Background(), db, migrationFilesFS(t, tt.name)))
			assertMigrationsRecorded(t, db, tt.name)
		})
	}
}

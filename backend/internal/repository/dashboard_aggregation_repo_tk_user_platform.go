package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// TK: per-(user, effective-platform, day) rollup feeder. Backs the two heaviest
// admin-page aggregations (GetBatchUserUsageStats / GetUserSpendingRanking) by
// pre-aggregating completed days so those pages stop scanning the full 30-day
// window of usage_logs (~1.25M rows / 845K buffers on prod). The live read path
// (see usage_log_repo_tk_user_platform_rollup.go) reads completed days from
// this table and today's partial day from raw usage_logs, so "today" numbers
// stay exact while history is served from the rollup.
//
// The effective-platform projection is the SAME usageLogEffectivePlatformExpr
// the live queries use (same package), so the rollup partitions by exactly the
// platform the page shows -- no second copy to keep in sync.

// upsertUserPlatformDailyAggregates rebuilds the per-(user, platform, day) rows
// for the given local-day window directly from raw usage_logs. requests and all
// token/cost sums include every row (actual_cost = 0 included) because the
// ranking consumer counts them unconditionally; the billed-only consumer filters
// at read time. A NULL/empty effective platform is coalesced to the empty string
// (not dropped) so its cost still reaches the user total via the reads.
func (r *dashboardAggregationRepository) upsertUserPlatformDailyAggregates(ctx context.Context, dayStart, dayEnd time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH per_row AS (
			SELECT
				(ul.created_at AT TIME ZONE $3)::date AS bucket_date,
				ul.user_id AS user_id,
				-- A NULL/empty effective platform (no group platform and no/blank
				-- account platform) is coalesced to '' rather than dropped: the
				-- ranking read sums across ALL platforms and the batch read folds
				-- '' into the user total (just not into the per-platform breakdown),
				-- so completed-day totals match the legacy "count every row"
				-- semantics. platform is part of the PK, so it cannot be NULL.
				COALESCE(` + usageLogEffectivePlatformExpr + `, '') AS platform,
				ul.input_tokens AS input_tokens,
				ul.output_tokens AS output_tokens,
				ul.cache_creation_tokens AS cache_creation_tokens,
				ul.cache_read_tokens AS cache_read_tokens,
				ul.actual_cost AS actual_cost
			FROM usage_logs ul
			LEFT JOIN groups g ON g.id = ul.group_id
			LEFT JOIN accounts a ON a.id = ul.account_id
			WHERE ul.created_at >= $1 AND ul.created_at < $2
		),
		rolled AS (
			SELECT
				bucket_date,
				user_id,
				platform,
				COUNT(*) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(actual_cost), 0) AS actual_cost
			FROM per_row
			GROUP BY bucket_date, user_id, platform
		)
		INSERT INTO usage_dashboard_user_platform_daily (
			bucket_date,
			user_id,
			platform,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			actual_cost,
			computed_at
		)
		SELECT
			bucket_date,
			user_id,
			platform,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			actual_cost,
			NOW()
		FROM rolled
		ON CONFLICT (bucket_date, user_id, platform)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			actual_cost = EXCLUDED.actual_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, dayStart, dayEnd, tzName)
	return err
}

// deleteUserPlatformDailyRange clears the per-(user, platform, day) rows for a
// local-day window before a recompute, so a row that dropped to zero (e.g. logs
// were deleted) does not linger. Mirrors the recompute discipline of the
// upstream usage_dashboard_daily rebuild.
func (r *dashboardAggregationRepository) deleteUserPlatformDailyRange(ctx context.Context, dayStart, dayEnd time.Time) error {
	_, err := r.sql.ExecContext(ctx,
		"DELETE FROM usage_dashboard_user_platform_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date",
		dayStart, dayEnd,
	)
	return err
}

// cleanupUserPlatformDaily prunes rollup rows older than the daily retention
// cutoff, matching CleanupAggregates' treatment of usage_dashboard_daily.
func (r *dashboardAggregationRepository) cleanupUserPlatformDaily(ctx context.Context, dailyCutoff time.Time) error {
	_, err := r.sql.ExecContext(ctx,
		"DELETE FROM usage_dashboard_user_platform_daily WHERE bucket_date < $1::date",
		dailyCutoff.UTC(),
	)
	return err
}

package repository

import (
	"context"
	"log"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// TK: per-(group, day) rollup feeder. Backs the admin Groups page usage-summary
// widget (GET /api/v1/admin/groups/usage-summary), whose legacy query SUMs
// actual_cost over the ENTIRE usage_logs table on every load, and the admin
// Dashboard/Usage group-distribution chart. The live read paths sum completed
// days from this rollup and read only partial/today slices from raw usage_logs,
// so wide windows are served without a full raw scan.
//
// bucket_date is computed in the configured server timezone (timezone.Name()) so
// the rollup/today split in the read path lines up exactly with the rollup grain.

// upsertGroupDailyAggregates rebuilds the per-(group, day) rows for the given
// server-TZ day window directly from raw usage_logs. Mirrors
// upsertUserPlatformDailyAggregates: the window is day-aligned so each day's SUM
// is complete, and ON CONFLICT replaces (not adds) so re-running a day is
// idempotent. Rows with NULL group_id are stored under group_id=0 so the
// Dashboard/Usage group-distribution chart preserves the legacy
// COALESCE(ul.group_id, 0) bucket. The Groups usage-summary read path filters
// back down to real group ids.
func (r *dashboardAggregationRepository) upsertGroupDailyAggregates(ctx context.Context, dayStart, dayEnd time.Time) error {
	tzName := timezone.Name()
	query := `
		INSERT INTO usage_dashboard_group_daily (
			bucket_date,
			group_id,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			computed_at
		)
		SELECT
			(ul.created_at AT TIME ZONE $3)::date AS bucket_date,
			COALESCE(ul.group_id, 0) AS group_id,
			COUNT(*) AS total_requests,
			COALESCE(SUM(ul.input_tokens), 0) AS input_tokens,
			COALESCE(SUM(ul.output_tokens), 0) AS output_tokens,
			COALESCE(SUM(ul.cache_creation_tokens), 0) AS cache_creation_tokens,
			COALESCE(SUM(ul.cache_read_tokens), 0) AS cache_read_tokens,
			COALESCE(SUM(ul.total_cost), 0) AS total_cost,
			COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
			COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS account_cost,
			NOW()
		FROM usage_logs ul
		WHERE ul.created_at >= $1 AND ul.created_at < $2
		GROUP BY 1, COALESCE(ul.group_id, 0)
		ON CONFLICT (bucket_date, group_id)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, dayStart, dayEnd, tzName)
	return err
}

// deleteGroupDailyRange clears the per-(group, day) rows for a server-TZ day
// window before a recompute, so a (group, day) whose rows were all deleted does
// not leave a stale rollup row behind. Mirrors deleteUserPlatformDailyRange.
func (r *dashboardAggregationRepository) deleteGroupDailyRange(ctx context.Context, dayStart, dayEnd time.Time) error {
	_, err := r.sql.ExecContext(ctx,
		"DELETE FROM usage_dashboard_group_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date",
		dayStart, dayEnd,
	)
	return err
}

// groupDailyBackfillMarkerDate is the reserved bucket_date of the sentinel row
// (group_id = 0) that records "the one-time historical backfill has run".
// group_id=0 is also used for real ungrouped usage on normal dates, so rollup
// read paths exclude these reserved marker dates explicitly.
const groupDailyBackfillMarkerDate = "1970-01-01"
const groupDailyMetricsBackfillMarkerDate = "1970-01-02"

// backfillGroupDailyAllOnce does a ONE-TIME full-history aggregation of the
// per-(group, day) rollup, in the runtime server timezone. This is required
// because the watermark-driven incremental feeder only ever moves forward and
// never backfills pre-deploy days — so without this the all-time Groups total
// would forever exclude historical cost (or force the read path to keep
// raw-scanning the whole table).
//
// It is guarded by a sentinel marker row rather than by table-emptiness: a
// deployment that has usage_logs but zero *grouped* usage would leave the rollup
// empty forever, and an emptiness guard would then re-run the full-table backfill
// scan every aggregation cycle. The marker decouples "backfill done" from "table
// has data". The read path falls back to a raw scan until the marker is set, so
// timing is not critical and a transient failure simply retries next cycle.
func (r *dashboardAggregationRepository) backfillGroupDailyAllOnce(ctx context.Context) error {
	var done bool
	if err := scanSingleRow(ctx, r.sql,
		"SELECT EXISTS(SELECT 1 FROM usage_dashboard_group_daily WHERE group_id = 0 AND bucket_date = DATE '"+groupDailyBackfillMarkerDate+"')",
		nil, &done); err != nil {
		return err
	}
	if done {
		return nil
	}
	if !hasDashboardHistoricalBackfillBudget(ctx) {
		log.Printf("[DashboardAggregation] group daily rollup backfill deferred: context deadline too close")
		return nil
	}
	tzName := timezone.Name()
	if _, err := r.sql.ExecContext(ctx, `
		INSERT INTO usage_dashboard_group_daily (
			bucket_date,
			group_id,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			computed_at
		)
		SELECT
			(ul.created_at AT TIME ZONE $1)::date AS bucket_date,
			COALESCE(ul.group_id, 0) AS group_id,
			COUNT(*) AS total_requests,
			COALESCE(SUM(ul.input_tokens), 0) AS input_tokens,
			COALESCE(SUM(ul.output_tokens), 0) AS output_tokens,
			COALESCE(SUM(ul.cache_creation_tokens), 0) AS cache_creation_tokens,
			COALESCE(SUM(ul.cache_read_tokens), 0) AS cache_read_tokens,
			COALESCE(SUM(ul.total_cost), 0) AS total_cost,
			COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
			COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS account_cost,
			NOW()
		FROM usage_logs ul
		GROUP BY 1, COALESCE(ul.group_id, 0)
		ON CONFLICT (bucket_date, group_id)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`, tzName); err != nil {
		return err
	}
	// Set both markers: this backfill now writes the full metric set, so a fresh
	// deployment must not immediately run the metrics-only backfill again.
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO usage_dashboard_group_daily (bucket_date, group_id, actual_cost, computed_at)
		VALUES
			(DATE '`+groupDailyBackfillMarkerDate+`', 0, 0, NOW()),
			(DATE '`+groupDailyMetricsBackfillMarkerDate+`', 0, 0, NOW())
		ON CONFLICT (bucket_date, group_id) DO NOTHING
	`)
	return err
}

// backfillGroupDailyMetricsAllOnce exists for deployments that already ran
// tk_038 when usage_dashboard_group_daily contained only actual_cost. tk_046 adds
// metric columns with default zero, so the group-distribution read path must not
// trust historical rows until this one-time rewrite has populated requests,
// tokens, total_cost, and account_cost.
func (r *dashboardAggregationRepository) backfillGroupDailyMetricsAllOnce(ctx context.Context) error {
	var done bool
	if err := scanSingleRow(ctx, r.sql,
		"SELECT EXISTS(SELECT 1 FROM usage_dashboard_group_daily WHERE group_id = 0 AND bucket_date = DATE '"+groupDailyMetricsBackfillMarkerDate+"')",
		nil, &done); err != nil {
		return err
	}
	if done {
		return nil
	}
	if !hasDashboardHistoricalBackfillBudget(ctx) {
		log.Printf("[DashboardAggregation] group daily metrics backfill deferred: context deadline too close")
		return nil
	}
	tzName := timezone.Name()
	if _, err := r.sql.ExecContext(ctx, `
		INSERT INTO usage_dashboard_group_daily (
			bucket_date,
			group_id,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			computed_at
		)
		SELECT
			(ul.created_at AT TIME ZONE $1)::date AS bucket_date,
			COALESCE(ul.group_id, 0) AS group_id,
			COUNT(*) AS total_requests,
			COALESCE(SUM(ul.input_tokens), 0) AS input_tokens,
			COALESCE(SUM(ul.output_tokens), 0) AS output_tokens,
			COALESCE(SUM(ul.cache_creation_tokens), 0) AS cache_creation_tokens,
			COALESCE(SUM(ul.cache_read_tokens), 0) AS cache_read_tokens,
			COALESCE(SUM(ul.total_cost), 0) AS total_cost,
			COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
			COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS account_cost,
			NOW()
		FROM usage_logs ul
		GROUP BY 1, COALESCE(ul.group_id, 0)
		ON CONFLICT (bucket_date, group_id)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			computed_at = EXCLUDED.computed_at
	`, tzName); err != nil {
		return err
	}
	_, err := r.sql.ExecContext(ctx,
		"INSERT INTO usage_dashboard_group_daily (bucket_date, group_id, actual_cost, computed_at) VALUES (DATE '"+groupDailyMetricsBackfillMarkerDate+"', 0, 0, NOW()) ON CONFLICT (bucket_date, group_id) DO NOTHING")
	return err
}

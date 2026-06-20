package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// TK: per-(group, day) cost rollup feeder. Backs the admin Groups page
// usage-summary widget (GET /api/v1/admin/groups/usage-summary), whose legacy
// query SUMs actual_cost over the ENTIRE usage_logs table on every load. The
// live read path (usage_log_repo_tk_group_rollup.go) sums completed days from
// this rollup and reads only today's partial day from raw usage_logs, so the
// all-time total is served without the full-table scan.
//
// bucket_date is computed in the configured server timezone (timezone.Name()) so
// the rollup/today split in the read path lines up exactly with the rollup grain.

// upsertGroupDailyAggregates rebuilds the per-(group, day) cost rows for the given
// server-TZ day window directly from raw usage_logs. Mirrors
// upsertUserPlatformDailyAggregates: the window is day-aligned so each day's SUM
// is complete, and ON CONFLICT replaces (not adds) so re-running a day is
// idempotent. Rows with NULL group_id are excluded (the legacy query's
// groups-LEFT-JOIN-usage_logs only sees rows whose group_id matches a group).
func (r *dashboardAggregationRepository) upsertGroupDailyAggregates(ctx context.Context, dayStart, dayEnd time.Time) error {
	tzName := timezone.Name()
	query := `
		INSERT INTO usage_dashboard_group_daily (bucket_date, group_id, actual_cost, computed_at)
		SELECT
			(ul.created_at AT TIME ZONE $3)::date AS bucket_date,
			ul.group_id,
			COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
			NOW()
		FROM usage_logs ul
		WHERE ul.created_at >= $1 AND ul.created_at < $2 AND ul.group_id IS NOT NULL
		GROUP BY 1, ul.group_id
		ON CONFLICT (bucket_date, group_id)
		DO UPDATE SET
			actual_cost = EXCLUDED.actual_cost,
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
// (group_id = 0, a value no real group uses) that records "the one-time
// historical backfill has run". The read path filters group_id > 0, so the
// marker never affects any group's total.
const groupDailyBackfillMarkerDate = "1970-01-01"

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
	tzName := timezone.Name()
	if _, err := r.sql.ExecContext(ctx, `
		INSERT INTO usage_dashboard_group_daily (bucket_date, group_id, actual_cost, computed_at)
		SELECT
			(ul.created_at AT TIME ZONE $1)::date AS bucket_date,
			ul.group_id,
			COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
			NOW()
		FROM usage_logs ul
		WHERE ul.group_id IS NOT NULL
		GROUP BY 1, ul.group_id
		ON CONFLICT (bucket_date, group_id)
		DO UPDATE SET
			actual_cost = EXCLUDED.actual_cost,
			computed_at = EXCLUDED.computed_at
	`, tzName); err != nil {
		return err
	}
	// Set the backfill-done marker so the full-table scan above never runs again.
	_, err := r.sql.ExecContext(ctx,
		"INSERT INTO usage_dashboard_group_daily (bucket_date, group_id, actual_cost, computed_at) VALUES (DATE '"+groupDailyBackfillMarkerDate+"', 0, 0, NOW()) ON CONFLICT (bucket_date, group_id) DO NOTHING")
	return err
}

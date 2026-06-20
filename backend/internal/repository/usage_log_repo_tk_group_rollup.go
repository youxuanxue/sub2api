package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

// groupUsageSummaryFromRollup answers GetAllGroupUsageSummary from the per-(group,
// day) rollup (usage_dashboard_group_daily) for completed server-TZ days plus raw
// usage_logs for the partial trailing day, instead of SUMming the ENTIRE
// usage_logs table on every GroupsView load. Returns ok=false (no error) when the
// rollup has not been backfilled yet, so the caller falls back to the legacy raw
// scan until the one-time backfill (see dashboard_aggregation_repo_tk_group.go)
// completes; the result then self-heals to this fast path.
//
// Exact-semantics decomposition (counts every usage_logs row exactly once toward
// total_cost, for ANY user timezone):
//
//	total_cost[g] = SUM(rollup.actual_cost WHERE bucket_date < serverToday)   -- completed server-TZ days
//	              + SUM(raw.actual_cost    WHERE created_at  >= serverToday)   -- the remainder not yet rolled up
//	today_cost[g] = SUM(raw.actual_cost    WHERE created_at  >= userTodayStart)
//
// The rollup buckets by SERVER-TZ day, so the rollup/raw split for total_cost
// uses the server-TZ start-of-today; a row is in exactly one side (rollup if its
// server-TZ date < today, raw otherwise). today_cost uses the caller's user-TZ
// todayStart, which may differ from the server TZ — both are narrow, index-bounded
// reads of recent usage_logs (createdAt index), unlike the legacy full-table scan.
func (r *usageLogRepository) groupUsageSummaryFromRollup(ctx context.Context, todayStart time.Time) ([]usagestats.GroupUsageSummary, bool, error) {
	var done bool
	if err := scanSingleRow(ctx, r.sql,
		"SELECT EXISTS(SELECT 1 FROM usage_dashboard_group_daily WHERE group_id = 0 AND bucket_date = DATE '"+groupDailyBackfillMarkerDate+"')",
		nil, &done); err != nil {
		// Degrade gracefully: a probe failure (e.g. the rollup table not yet
		// migrated in) must not break the Groups page — signal ok=false so the
		// caller uses the legacy raw scan, which does not touch this table.
		return nil, false, nil
	}
	if !done {
		return nil, false, nil
	}

	serverTodayStart := timezone.StartOfDay(timezone.Now())
	serverTodayDate := serverTodayStart.Format("2006-01-02")

	// $1 = server-TZ today date (rollup completed-day boundary)
	// $2 = server-TZ start-of-today instant (raw remainder boundary for total_cost)
	// $3 = caller's user-TZ start-of-today instant (raw boundary for today_cost)
	query := `
		SELECT
			g.id,
			COALESCE(rb.rollup_total, 0) + COALESCE(rr.raw_remainder, 0) AS total_cost,
			COALESCE(rr.today_cost, 0) AS today_cost
		FROM groups g
		LEFT JOIN (
			SELECT group_id, COALESCE(SUM(actual_cost), 0) AS rollup_total
			FROM usage_dashboard_group_daily
			WHERE group_id > 0 AND bucket_date < $1::date
			GROUP BY group_id
		) rb ON rb.group_id = g.id
		LEFT JOIN (
			SELECT
				group_id,
				COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $2), 0) AS raw_remainder,
				COALESCE(SUM(actual_cost) FILTER (WHERE created_at >= $3), 0) AS today_cost
			FROM usage_logs
			WHERE group_id IS NOT NULL AND created_at >= LEAST($2, $3)
			GROUP BY group_id
		) rr ON rr.group_id = g.id
	`
	rows, err := r.sql.QueryContext(ctx, query, serverTodayDate, serverTodayStart, todayStart)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()

	results := make([]usagestats.GroupUsageSummary, 0)
	for rows.Next() {
		var row usagestats.GroupUsageSummary
		if err := rows.Scan(&row.GroupID, &row.TotalCost, &row.TodayCost); err != nil {
			return nil, false, err
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return results, true, nil
}

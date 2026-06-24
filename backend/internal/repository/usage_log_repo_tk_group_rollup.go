package repository

import (
	"context"
	"database/sql"
	"sort"
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

type groupStatAgg struct {
	groupName           string
	requests            int64
	inputTokens         int64
	outputTokens        int64
	cacheCreationTokens int64
	cacheReadTokens     int64
	cost                float64
	actualCost          float64
	accountCost         float64
}

func shouldUseGroupDailyStatsRollup(userID, apiKeyID, accountID int64, requestType *int16, stream *bool, billingType *int8) bool {
	return userID == 0 &&
		apiKeyID == 0 &&
		accountID == 0 &&
		requestType == nil &&
		stream == nil &&
		billingType == nil
}

func (r *usageLogRepository) groupDailyMetricsBackfilled(ctx context.Context) (bool, error) {
	var done bool
	if err := scanSingleRow(ctx, r.sql,
		"SELECT EXISTS(SELECT 1 FROM usage_dashboard_group_daily WHERE group_id = 0 AND bucket_date = DATE '"+groupDailyMetricsBackfillMarkerDate+"')",
		nil, &done); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return done, nil
}

func (r *usageLogRepository) getGroupStatsFromRollup(
	ctx context.Context,
	startTime,
	endTime time.Time,
	groupID int64,
) ([]usagestats.GroupStat, bool, error) {
	if r.db == nil || !endTime.After(startTime) {
		return nil, false, nil
	}
	metricsReady, err := r.groupDailyMetricsBackfilled(ctx)
	if err != nil {
		return nil, false, err
	}
	if !metricsReady {
		return nil, false, nil
	}

	floorDay, hasRollupData, err := r.groupDailyRollupFloorDay(ctx)
	if err != nil {
		return nil, false, err
	}
	win := planUsageRollupWindow(startTime, endTime, floorDay, hasRollupData)
	byGroupID := make(map[int64]*groupStatAgg)

	addRow := func(groupID int64, groupName string, reqs, inTok, outTok, cacheCreate, cacheRead int64, cost, actualCost, accountCost float64) {
		a, ok := byGroupID[groupID]
		if !ok {
			a = &groupStatAgg{}
			byGroupID[groupID] = a
		}
		if groupName != "" {
			a.groupName = groupName
		}
		a.requests += reqs
		a.inputTokens += inTok
		a.outputTokens += outTok
		a.cacheCreationTokens += cacheCreate
		a.cacheReadTokens += cacheRead
		a.cost += cost
		a.actualCost += actualCost
		a.accountCost += accountCost
	}

	if win.hasRollup {
		query := `
			SELECT
				gd.group_id,
				COALESCE(g.name, '') AS group_name,
				COALESCE(SUM(gd.total_requests), 0),
				COALESCE(SUM(gd.input_tokens), 0),
				COALESCE(SUM(gd.output_tokens), 0),
				COALESCE(SUM(gd.cache_creation_tokens), 0),
				COALESCE(SUM(gd.cache_read_tokens), 0),
				COALESCE(SUM(gd.total_cost), 0),
				COALESCE(SUM(gd.actual_cost), 0),
				COALESCE(SUM(gd.account_cost), 0)
			FROM usage_dashboard_group_daily gd
			LEFT JOIN groups g ON g.id = gd.group_id
			WHERE gd.bucket_date > DATE '` + groupDailyMetricsBackfillMarkerDate + `'
			  AND gd.bucket_date >= $1::date AND gd.bucket_date < $2::date
		`
		args := []any{win.rollupStartDay, win.rollupEndDay}
		if groupID > 0 {
			query += " AND gd.group_id = $3"
			args = append(args, groupID)
		}
		query += " GROUP BY gd.group_id, g.name"

		rows, err := r.sql.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, false, err
		}
		for rows.Next() {
			var id int64
			var name string
			var reqs, inTok, outTok, cacheCreate, cacheRead int64
			var cost, actualCost, accountCost float64
			if err := rows.Scan(&id, &name, &reqs, &inTok, &outTok, &cacheCreate, &cacheRead, &cost, &actualCost, &accountCost); err != nil {
				_ = rows.Close()
				return nil, false, err
			}
			addRow(id, name, reqs, inTok, outTok, cacheCreate, cacheRead, cost, actualCost, accountCost)
		}
		if err := rows.Close(); err != nil {
			return nil, false, err
		}
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
	}

	for _, span := range win.rawSpans {
		from, to := span[0], span[1]
		query := `
			SELECT
				COALESCE(ul.group_id, 0) AS group_id,
				COALESCE(g.name, '') AS group_name,
				COUNT(*) AS requests,
				COALESCE(SUM(ul.input_tokens), 0),
				COALESCE(SUM(ul.output_tokens), 0),
				COALESCE(SUM(ul.cache_creation_tokens), 0),
				COALESCE(SUM(ul.cache_read_tokens), 0),
				COALESCE(SUM(ul.total_cost), 0),
				COALESCE(SUM(ul.actual_cost), 0),
				COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0)
			FROM usage_logs ul
			LEFT JOIN groups g ON g.id = ul.group_id
			WHERE ul.created_at >= $1 AND ul.created_at < $2
		`
		args := []any{from, to}
		if groupID > 0 {
			query += " AND ul.group_id = $3"
			args = append(args, groupID)
		}
		query += " GROUP BY ul.group_id, g.name"

		rows, err := r.sql.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, false, err
		}
		for rows.Next() {
			var id int64
			var name string
			var reqs, inTok, outTok, cacheCreate, cacheRead int64
			var cost, actualCost, accountCost float64
			if err := rows.Scan(&id, &name, &reqs, &inTok, &outTok, &cacheCreate, &cacheRead, &cost, &actualCost, &accountCost); err != nil {
				_ = rows.Close()
				return nil, false, err
			}
			addRow(id, name, reqs, inTok, outTok, cacheCreate, cacheRead, cost, actualCost, accountCost)
		}
		if err := rows.Close(); err != nil {
			return nil, false, err
		}
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
	}

	results := make([]usagestats.GroupStat, 0, len(byGroupID))
	for id, a := range byGroupID {
		results = append(results, usagestats.GroupStat{
			GroupID:     id,
			GroupName:   a.groupName,
			Requests:    a.requests,
			TotalTokens: a.inputTokens + a.outputTokens + a.cacheCreationTokens + a.cacheReadTokens,
			Cost:        a.cost,
			ActualCost:  a.actualCost,
			AccountCost: a.accountCost,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].TotalTokens == results[j].TotalTokens {
			return results[i].GroupID < results[j].GroupID
		}
		return results[i].TotalTokens > results[j].TotalTokens
	})
	return results, true, nil
}

func (r *usageLogRepository) groupDailyRollupFloorDay(ctx context.Context) (time.Time, bool, error) {
	var s sql.NullString
	if err := scanSingleRow(ctx, r.sql,
		`SELECT to_char(MIN(bucket_date), 'YYYY-MM-DD') FROM usage_dashboard_group_daily WHERE bucket_date > DATE '`+groupDailyMetricsBackfillMarkerDate+`'`,
		nil, &s); err != nil {
		return time.Time{}, false, err
	}
	if !s.Valid || s.String == "" {
		return time.Time{}, false, nil
	}
	loc := timezone.Today().Location()
	day, err := time.ParseInLocation("2006-01-02", s.String, loc)
	if err != nil {
		return time.Time{}, false, err
	}
	return day, true, nil
}

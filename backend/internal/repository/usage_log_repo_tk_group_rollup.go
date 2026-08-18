package repository

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

type groupStatAgg struct {
	groupName                            string
	requests                             int64
	inputTokens                          int64
	outputTokens                         int64
	cacheCreationTokens                  int64
	cacheReadTokens                      int64
	cacheTelemetryUnavailableInputTokens int64
	cost                                 float64
	actualCost                           float64
	accountCost                          float64
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

	// billing_tier is not part of the daily rollup. Read only Kiro-estimated rows
	// from the selected raw window so their input is never
	// mistaken for observable cache telemetry, without widening the rollup schema.
	telemetryQuery := `
		SELECT
			COALESCE(ul.group_id, 0) AS group_id,
			COALESCE(SUM(ul.input_tokens), 0)
		FROM usage_logs ul
		WHERE ul.created_at >= $1 AND ul.created_at < $2
		  AND ul.billing_tier = 'kiro-estimated'
	`
	telemetryArgs := []any{startTime, endTime}
	if groupID > 0 {
		telemetryQuery += " AND ul.group_id = $3"
		telemetryArgs = append(telemetryArgs, groupID)
	}
	telemetryQuery += " GROUP BY ul.group_id"
	telemetryRows, err := r.sql.QueryContext(ctx, telemetryQuery, telemetryArgs...)
	if err != nil {
		return nil, false, err
	}
	for telemetryRows.Next() {
		var id, unavailableInput int64
		if err := telemetryRows.Scan(&id, &unavailableInput); err != nil {
			_ = telemetryRows.Close()
			return nil, false, err
		}
		a, ok := byGroupID[id]
		if !ok {
			a = &groupStatAgg{}
			byGroupID[id] = a
		}
		a.cacheTelemetryUnavailableInputTokens = unavailableInput
	}
	if err := telemetryRows.Close(); err != nil {
		return nil, false, err
	}
	if err := telemetryRows.Err(); err != nil {
		return nil, false, err
	}

	results := make([]usagestats.GroupStat, 0, len(byGroupID))
	for id, a := range byGroupID {
		results = append(results, usagestats.GroupStat{
			GroupID:                              id,
			GroupName:                            a.groupName,
			Requests:                             a.requests,
			InputTokens:                          a.inputTokens,
			OutputTokens:                         a.outputTokens,
			CacheCreationTokens:                  a.cacheCreationTokens,
			CacheReadTokens:                      a.cacheReadTokens,
			CacheTelemetryUnavailableInputTokens: a.cacheTelemetryUnavailableInputTokens,
			TotalTokens:                          a.inputTokens + a.outputTokens + a.cacheCreationTokens + a.cacheReadTokens,
			Cost:                                 a.cost,
			ActualCost:                           a.actualCost,
			AccountCost:                          a.accountCost,
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

package repository

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

type modelStatAgg struct {
	requests            int64
	inputTokens         int64
	outputTokens        int64
	cacheCreationTokens int64
	cacheReadTokens     int64
	totalTokens         int64
	cost                float64
	actualCost          float64
	accountCost         float64
}

func shouldUseModelDailyRollup(
	userID, apiKeyID, accountID, groupID int64,
	requestType *int16,
	stream *bool,
	billingType *int8,
	source string,
) bool {
	if usagestats.NormalizeModelSource(source) != usagestats.ModelSourceRequested {
		return false
	}
	return userID == 0 &&
		apiKeyID == 0 &&
		accountID == 0 &&
		groupID == 0 &&
		requestType == nil &&
		stream == nil &&
		billingType == nil
}

func (r *usageLogRepository) modelDailyRollupFloorDay(ctx context.Context) (time.Time, bool, error) {
	rows, err := r.sql.QueryContext(ctx,
		`SELECT to_char(MIN(bucket_date), 'YYYY-MM-DD') FROM usage_dashboard_model_daily`)
	if err != nil {
		return time.Time{}, false, err
	}
	var s sql.NullString
	for rows.Next() {
		if err := rows.Scan(&s); err != nil {
			_ = rows.Close()
			return time.Time{}, false, err
		}
	}
	if err := rows.Close(); err != nil {
		return time.Time{}, false, err
	}
	if err := rows.Err(); err != nil {
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

func (r *usageLogRepository) getModelStatsFromRollup(
	ctx context.Context,
	startTime, endTime time.Time,
) ([]ModelStat, error) {
	floorDay, hasRollupData, err := r.modelDailyRollupFloorDay(ctx)
	if err != nil {
		return nil, err
	}
	win := planUsageRollupWindow(startTime, endTime, floorDay, hasRollupData)
	byModel := make(map[string]*modelStatAgg)

	addRow := func(model string, reqs, inTok, outTok, cacheCreate, cacheRead, totalTok int64, cost, actualCost, accountCost float64) {
		a, ok := byModel[model]
		if !ok {
			a = &modelStatAgg{}
			byModel[model] = a
		}
		a.requests += reqs
		a.inputTokens += inTok
		a.outputTokens += outTok
		a.cacheCreationTokens += cacheCreate
		a.cacheReadTokens += cacheRead
		a.totalTokens += totalTok
		a.cost += cost
		a.actualCost += actualCost
		a.accountCost += accountCost
	}

	if win.hasRollup {
		const q = `
			SELECT
				model,
				COALESCE(SUM(total_requests), 0),
				COALESCE(SUM(input_tokens), 0),
				COALESCE(SUM(output_tokens), 0),
				COALESCE(SUM(cache_creation_tokens), 0),
				COALESCE(SUM(cache_read_tokens), 0),
				COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0),
				COALESCE(SUM(total_cost), 0),
				COALESCE(SUM(actual_cost), 0),
				COALESCE(SUM(account_cost), 0)
			FROM usage_dashboard_model_daily
			WHERE bucket_date >= $1::date AND bucket_date < $2::date
			GROUP BY model
		`
		rows, err := r.sql.QueryContext(ctx, q, win.rollupStartDay, win.rollupEndDay)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var model string
			var reqs, inTok, outTok, cacheCreate, cacheRead, totalTok int64
			var cost, actualCost, accountCost float64
			if err := rows.Scan(&model, &reqs, &inTok, &outTok, &cacheCreate, &cacheRead, &totalTok, &cost, &actualCost, &accountCost); err != nil {
				_ = rows.Close()
				return nil, err
			}
			addRow(model, reqs, inTok, outTok, cacheCreate, cacheRead, totalTok, cost, actualCost, accountCost)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	for _, span := range win.rawSpans {
		from, to := span[0], span[1]
		const q = `
			SELECT
				COALESCE(NULLIF(TRIM(requested_model), ''), model) AS model,
				COUNT(*) AS requests,
				COALESCE(SUM(input_tokens), 0),
				COALESCE(SUM(output_tokens), 0),
				COALESCE(SUM(cache_creation_tokens), 0),
				COALESCE(SUM(cache_read_tokens), 0),
				COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0),
				COALESCE(SUM(total_cost), 0),
				COALESCE(SUM(actual_cost), 0),
				COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0)
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY model
		`
		rows, err := r.sql.QueryContext(ctx, q, from, to)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var model string
			var reqs, inTok, outTok, cacheCreate, cacheRead, totalTok int64
			var cost, actualCost, accountCost float64
			if err := rows.Scan(&model, &reqs, &inTok, &outTok, &cacheCreate, &cacheRead, &totalTok, &cost, &actualCost, &accountCost); err != nil {
				_ = rows.Close()
				return nil, err
			}
			addRow(model, reqs, inTok, outTok, cacheCreate, cacheRead, totalTok, cost, actualCost, accountCost)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	results := make([]ModelStat, 0, len(byModel))
	for model, a := range byModel {
		results = append(results, ModelStat{
			Model:               model,
			Requests:            a.requests,
			InputTokens:         a.inputTokens,
			OutputTokens:        a.outputTokens,
			CacheCreationTokens: a.cacheCreationTokens,
			CacheReadTokens:     a.cacheReadTokens,
			TotalTokens:         a.totalTokens,
			Cost:                a.cost,
			ActualCost:          a.actualCost,
			AccountCost:         a.accountCost,
		})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].TotalTokens != results[j].TotalTokens {
			return results[i].TotalTokens > results[j].TotalTokens
		}
		return results[i].Model < results[j].Model
	})
	return results, nil
}

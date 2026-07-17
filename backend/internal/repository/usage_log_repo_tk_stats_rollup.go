package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

type usageStatsRollupAgg struct {
	TotalRequests       int64
	TotalInputTokens    int64
	TotalOutputTokens   int64
	TotalCacheTokens    int64
	TotalCost           float64
	TotalActualCost     float64
	totalAccountCost    float64
	AverageDurationMs   float64
	totalDurationMillis int64
}

func shouldUseUsageStatsHourlyRollup(filters usagestats.UsageLogFilters) bool {
	return filters.UserID == 0 &&
		filters.APIKeyID == 0 &&
		filters.AccountID == 0 &&
		filters.GroupID == 0 &&
		filters.Model == "" &&
		filters.RequestType == nil &&
		filters.Stream == nil &&
		filters.BillingType == nil &&
		filters.BillingMode == ""
}

func floorHourServerTZ(t time.Time) time.Time {
	loc := timezone.Location()
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, loc)
}

func ceilHourServerTZ(t time.Time) time.Time {
	floor := floorHourServerTZ(t)
	if floor.Equal(t.In(floor.Location())) {
		return floor
	}
	return floor.Add(time.Hour)
}

func (r *usageLogRepository) getStatsWithFiltersFromHourlyRollup(
	ctx context.Context,
	filters usagestats.UsageLogFilters,
	start,
	end time.Time,
) (*usageStatsRollupAgg, bool, error) {
	if r.db == nil || !shouldUseUsageStatsHourlyRollup(filters) || !end.After(start) {
		return nil, false, nil
	}

	rollupStart := ceilHourServerTZ(start)
	rollupEnd := floorHourServerTZ(end)
	if !rollupEnd.After(rollupStart) {
		return nil, false, nil
	}

	floor, hasRollupData, err := r.usageStatsHourlyRollupFloor(ctx)
	if err != nil {
		return nil, false, err
	}
	if !hasRollupData {
		return nil, false, nil
	}
	if floor.After(rollupStart) {
		rollupStart = floor
	}
	if !rollupEnd.After(rollupStart) {
		return nil, false, nil
	}

	var watermark time.Time
	if err := scanSingleRow(ctx, r.sql, "SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark WHERE id = 1", nil, &watermark); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	watermarkHour := floorHourServerTZ(watermark)
	if watermarkHour.Before(rollupEnd) {
		rollupEnd = watermarkHour
	}
	if !rollupEnd.After(rollupStart) {
		return nil, false, nil
	}

	agg := &usageStatsRollupAgg{}
	if err := r.addUsageStatsHourlyRollup(ctx, agg, rollupStart, rollupEnd); err != nil {
		return nil, false, err
	}
	if rollupStart.After(start) {
		if err := r.addUsageStatsRawSpan(ctx, agg, start, rollupStart); err != nil {
			return nil, false, err
		}
	}
	if end.After(rollupEnd) {
		if err := r.addUsageStatsRawSpan(ctx, agg, rollupEnd, end); err != nil {
			return nil, false, err
		}
	}
	if agg.TotalRequests > 0 {
		agg.AverageDurationMs = float64(agg.totalDurationMillis) / float64(agg.TotalRequests)
	}
	return agg, true, nil
}

func (r *usageLogRepository) usageStatsHourlyRollupFloor(ctx context.Context) (time.Time, bool, error) {
	var floor sql.NullTime
	if err := scanSingleRow(ctx, r.sql, `SELECT MIN(bucket_start) FROM usage_dashboard_hourly`, nil, &floor); err != nil {
		return time.Time{}, false, err
	}
	if !floor.Valid {
		return time.Time{}, false, nil
	}
	return floorHourServerTZ(floor.Time), true, nil
}

func (r *usageLogRepository) addUsageStatsHourlyRollup(ctx context.Context, agg *usageStatsRollupAgg, start, end time.Time) error {
	const q = `
		SELECT
			COALESCE(SUM(total_requests), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0),
			COALESCE(SUM(total_cost), 0),
			COALESCE(SUM(actual_cost), 0),
			COALESCE(SUM(account_cost), 0),
			COALESCE(SUM(total_duration_ms), 0)
		FROM usage_dashboard_hourly
		WHERE bucket_start >= $1 AND bucket_start < $2
	`
	var row usageStatsRollupAgg
	if err := scanSingleRow(
		ctx,
		r.sql,
		q,
		[]any{start, end},
		&row.TotalRequests,
		&row.TotalInputTokens,
		&row.TotalOutputTokens,
		&row.TotalCacheTokens,
		&row.TotalCost,
		&row.TotalActualCost,
		&row.totalAccountCost,
		&row.totalDurationMillis,
	); err != nil {
		return err
	}
	mergeUsageStatsAgg(agg, &row)
	return nil
}

func (r *usageLogRepository) addUsageStatsRawSpan(ctx context.Context, agg *usageStatsRollupAgg, start, end time.Time) error {
	if !end.After(start) {
		return nil
	}
	const q = `
		SELECT
			COUNT(*),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens + cache_read_tokens), 0),
			COALESCE(SUM(total_cost), 0),
			COALESCE(SUM(actual_cost), 0),
			COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0),
			COALESCE(SUM(COALESCE(duration_ms, 0)), 0)
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
	`
	var row usageStatsRollupAgg
	if err := scanSingleRow(
		ctx,
		r.sql,
		q,
		[]any{start, end},
		&row.TotalRequests,
		&row.TotalInputTokens,
		&row.TotalOutputTokens,
		&row.TotalCacheTokens,
		&row.TotalCost,
		&row.TotalActualCost,
		&row.totalAccountCost,
		&row.totalDurationMillis,
	); err != nil {
		return err
	}
	mergeUsageStatsAgg(agg, &row)
	return nil
}

func mergeUsageStatsAgg(dst, src *usageStatsRollupAgg) {
	dst.TotalRequests += src.TotalRequests
	dst.TotalInputTokens += src.TotalInputTokens
	dst.TotalOutputTokens += src.TotalOutputTokens
	dst.TotalCacheTokens += src.TotalCacheTokens
	dst.TotalCost += src.TotalCost
	dst.TotalActualCost += src.TotalActualCost
	dst.totalAccountCost += src.totalAccountCost
	dst.totalDurationMillis += src.totalDurationMillis
}

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestFillDashboardUsageStatsFromUsageLogs_AverageGatewayLatencyMs(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db, db: db}

	startUTC := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	endUTC := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	todayUTC := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	todayEnd := todayUTC.Add(24 * time.Hour)
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("FROM scoped").
		WithArgs(startUTC, endUTC, todayUTC, todayEnd).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens",
			"total_cache_creation_tokens", "total_cache_read_tokens",
			"total_cost", "total_actual_cost", "total_account_cost", "total_duration_ms",
			"total_gateway_latency_ms", "gateway_latency_samples",
			"today_requests", "today_input_tokens", "today_output_tokens",
			"today_cache_creation_tokens", "today_cache_read_tokens",
			"today_cost", "today_actual_cost", "today_account_cost",
		}).AddRow(
			int64(4), int64(0), int64(0), int64(0), int64(0),
			0.0, 0.0, 0.0, int64(4000),
			int64(600), int64(2),
			int64(1), int64(0), int64(0), int64(0), int64(0),
			0.0, 0.0, 0.0,
		))

	hourStart := now.UTC().Truncate(time.Hour)
	hourEnd := hourStart.Add(time.Hour)
	mock.ExpectQuery("active_users").
		WithArgs(todayUTC, todayEnd, hourStart, hourEnd).
		WillReturnRows(sqlmock.NewRows([]string{"active_users", "hourly_active_users"}).AddRow(int64(1), int64(1)))

	stats := &DashboardStats{}
	err := repo.fillDashboardUsageStatsFromUsageLogs(context.Background(), stats, startUTC, endUTC, todayUTC, now)
	require.NoError(t, err)
	require.InDelta(t, 300.0, stats.AverageGatewayLatencyMs, 1e-9)
	require.InDelta(t, 1000.0, stats.AverageDurationMs, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFillDashboardUsageStatsFromUsageLogs_AverageGatewayLatencyMsIgnoresNullSamples(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db, db: db}

	startUTC := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	endUTC := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	todayUTC := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	todayEnd := todayUTC.Add(24 * time.Hour)
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("FROM scoped").
		WithArgs(startUTC, endUTC, todayUTC, todayEnd).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens",
			"total_cache_creation_tokens", "total_cache_read_tokens",
			"total_cost", "total_actual_cost", "total_account_cost", "total_duration_ms",
			"total_gateway_latency_ms", "gateway_latency_samples",
			"today_requests", "today_input_tokens", "today_output_tokens",
			"today_cache_creation_tokens", "today_cache_read_tokens",
			"today_cost", "today_actual_cost", "today_account_cost",
		}).AddRow(
			int64(3), int64(0), int64(0), int64(0), int64(0),
			0.0, 0.0, 0.0, int64(900),
			int64(0), int64(0),
			int64(0), int64(0), int64(0), int64(0), int64(0),
			0.0, 0.0, 0.0,
		))

	hourStart := now.UTC().Truncate(time.Hour)
	hourEnd := hourStart.Add(time.Hour)
	mock.ExpectQuery("active_users").
		WithArgs(todayUTC, todayEnd, hourStart, hourEnd).
		WillReturnRows(sqlmock.NewRows([]string{"active_users", "hourly_active_users"}).AddRow(int64(0), int64(0)))

	stats := &DashboardStats{}
	err := repo.fillDashboardUsageStatsFromUsageLogs(context.Background(), stats, startUTC, endUTC, todayUTC, now)
	require.NoError(t, err)
	require.Zero(t, stats.AverageGatewayLatencyMs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFillDashboardUsageStatsAggregated_AverageGatewayLatencyMs(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db, db: db}

	todayUTC := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("FROM usage_dashboard_daily").
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens",
			"total_cache_creation_tokens", "total_cache_read_tokens",
			"total_cost", "total_actual_cost", "total_account_cost", "total_duration_ms",
			"total_gateway_latency_ms", "gateway_latency_samples",
		}).AddRow(
			int64(5), int64(0), int64(0), int64(0), int64(0),
			0.0, 0.0, 0.0, int64(5000),
			int64(1000), int64(5),
		))

	mock.ExpectQuery("WHERE bucket_date = \\$1").
		WithArgs(todayUTC).
		WillReturnRows(sqlmock.NewRows([]string{
			"today_requests", "today_input_tokens", "today_output_tokens",
			"today_cache_creation_tokens", "today_cache_read_tokens",
			"today_cost", "today_actual_cost", "today_account_cost", "active_users",
		}).AddRow(int64(1), int64(0), int64(0), int64(0), int64(0), 0.0, 0.0, 0.0, int64(1)))

	hourStart := now.UTC().Truncate(time.Hour)
	mock.ExpectQuery("FROM usage_dashboard_hourly").
		WithArgs(hourStart).
		WillReturnRows(sqlmock.NewRows([]string{"hourly_active_users"}).AddRow(int64(1)))

	stats := &DashboardStats{}
	err := repo.fillDashboardUsageStatsAggregated(context.Background(), stats, todayUTC, now)
	require.NoError(t, err)
	require.InDelta(t, 200.0, stats.AverageGatewayLatencyMs, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}

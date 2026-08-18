package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestGetStatsWithFilters_AverageGatewayLatencyMsIgnoresNullSamples(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	filters := usagestats.UsageLogFilters{UserID: 16, StartTime: &start, EndTime: &end, SkipEndpointStats: true}

	mock.ExpectQuery("FROM usage_logs").
		WithArgs(int64(16), start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens",
			"total_cache_tokens", "total_cache_creation_tokens", "total_cache_read_tokens",
			"total_cost", "total_actual_cost",
			"total_account_cost", "avg_duration_ms", "avg_gateway_latency_ms",
		}).AddRow(int64(23), int64(100), int64(50), int64(0), int64(0), int64(0), 0.16, 0.16, 0.0, 10580.0, 28.0))

	stats, err := repo.GetStatsWithFilters(context.Background(), filters)
	require.NoError(t, err)
	require.InDelta(t, 10580.0, stats.AverageDurationMs, 1e-9)
	require.InDelta(t, 28.0, stats.AverageGatewayLatencyMs, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}

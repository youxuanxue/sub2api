package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGetUserDashboardStats_UsesTodayGatewayLatencyForLatencyCard(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))

	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	userID := int64(42)
	today := timezone.Today()

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM api_keys WHERE user_id = \\$1 AND deleted_at IS NULL").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM api_keys WHERE user_id = \\$1 AND status = \\$2 AND deleted_at IS NULL").
		WithArgs(userID, service.StatusActive).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))

	mock.ExpectQuery("avg_gateway_latency_ms").
		WithArgs(userID, today).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "total_input_tokens", "total_output_tokens",
			"total_cache_creation_tokens", "total_cache_read_tokens", "total_cost",
			"total_actual_cost", "avg_duration_ms", "avg_gateway_latency_ms",
		}).AddRow(int64(2), int64(10), int64(20), int64(0), int64(0), 1.0, 1.0, 13680.0, 42.0))

	mock.ExpectQuery("today_requests").
		WithArgs(userID, today).
		WillReturnRows(sqlmock.NewRows([]string{
			"today_requests", "today_input_tokens", "today_output_tokens",
			"today_cache_creation_tokens", "today_cache_read_tokens", "today_cost",
			"today_actual_cost",
		}).AddRow(int64(1), int64(5), int64(10), int64(0), int64(0), 0.5, 0.5))

	mock.ExpectQuery("request_count").
		WithArgs(sqlmock.AnyArg(), userID).
		WillReturnRows(sqlmock.NewRows([]string{"request_count", "token_count"}).AddRow(int64(0), int64(0)))

	mock.ExpectQuery("FROM usage_logs ul LEFT JOIN groups g ON g.id = ul.group_id").
		WithArgs(userID, today).
		WillReturnRows(sqlmock.NewRows([]string{
			"platform", "total_requests", "total_tokens", "total_actual_cost",
			"today_requests", "today_tokens", "today_actual_cost",
		}))

	stats, err := repo.GetUserDashboardStats(context.Background(), userID)
	require.NoError(t, err)
	require.InDelta(t, 13680.0, stats.AverageDurationMs, 1e-9)
	require.InDelta(t, 42.0, stats.AverageGatewayLatencyMs, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}

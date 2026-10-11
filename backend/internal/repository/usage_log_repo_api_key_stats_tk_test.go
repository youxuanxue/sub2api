package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryGetAPIKeyStatsWithUsageFilters(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	userID := int64(16)

	mock.ExpectQuery("FROM usage_logs ul").
		WithArgs(start, end, userID).
		WillReturnRows(sqlmock.NewRows([]string{
			"api_key_id", "api_key_name", "requests",
			"input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens",
			"cost", "actual_cost",
		}).
			AddRow(int64(11), "prod-key", int64(100), int64(1000), int64(2000), int64(100), int64(50), 12.0, 9.5).
			AddRow(int64(12), "dev-key", int64(10), int64(100), int64(200), int64(0), int64(0), 1.0, 0.8))

	results, err := repo.GetAPIKeyStatsWithUsageFilters(context.Background(), start, end, UsageLogFilters{UserID: userID})
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, int64(11), results[0].APIKeyID)
	require.Equal(t, "prod-key", results[0].APIKeyName)
	require.Equal(t, int64(3150), results[0].TotalTokens)
	require.Equal(t, 9.5, results[0].ActualCost)
	require.Equal(t, int64(12), results[1].APIKeyID)
	require.Equal(t, int64(300), results[1].TotalTokens)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetAPIKeyStatsWithUsageFiltersRejectsEmptyUserStillScopesByTime(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery("FROM usage_logs ul").
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"api_key_id", "api_key_name", "requests",
			"input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens",
			"cost", "actual_cost",
		}))

	results, err := repo.GetAPIKeyStatsWithUsageFilters(context.Background(), start, end, UsageLogFilters{})
	require.NoError(t, err)
	require.Empty(t, results)
	require.NoError(t, mock.ExpectationsWereMet())
}

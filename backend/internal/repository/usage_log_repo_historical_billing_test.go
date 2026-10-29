package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUS073_HistoricalBillingRepositorySnapshot(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	now := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
	columns := []string{"created_at", "timezone_name", "statement_timestamp", "date", "platform", "total_requests", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "cost", "computed_at"}
	mock.ExpectQuery("(?s)FROM users u.*d.user_id = u.id.*WHERE u.id = \\$1").WithArgs(int64(16)).WillReturnRows(sqlmock.NewRows(columns).AddRow(now, "UTC", now, "2026-06-04", "openai", 2, 10, 20, 30, 40, "0.1234567891", now))
	snapshot, err := repo.GetHistoricalBillingSnapshot(context.Background(), 16)
	require.NoError(t, err)
	require.Equal(t, "0.1234567891", snapshot.Rows[0].ActualCost)
	require.Equal(t, int64(30), snapshot.Rows[0].CacheCreationTokens)
	require.Equal(t, "2026-06-04", snapshot.Rows[0].Date)
	mock.ExpectQuery("FROM users u").WithArgs(int64(17)).WillReturnRows(sqlmock.NewRows(columns).AddRow(now, "UTC", now, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	empty, err := repo.GetHistoricalBillingSnapshot(context.Background(), 17)
	require.NoError(t, err)
	require.Empty(t, empty.Rows)
	require.Equal(t, now, empty.RegisteredAt)
	mock.ExpectQuery("FROM users u").WithArgs(int64(18)).WillReturnRows(sqlmock.NewRows(columns))
	_, err = repo.GetHistoricalBillingSnapshot(context.Background(), 18)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

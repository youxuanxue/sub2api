package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestGetBatchAPIKeyUsageStatsPrunesWithCreatedAtUpperBound(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}

	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	ids := []int64{11, 12}

	mock.ExpectQuery(`(?s)FROM usage_logs.*created_at < \$3.*GROUP BY api_key_id`).
		WithArgs(pq.Array(ids), start, end, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"api_key_id", "total_cost", "today_cost"}).
			AddRow(int64(11), 1.5, 0.25).
			AddRow(int64(12), 2.0, 0.0))

	stats, err := repo.GetBatchAPIKeyUsageStats(context.Background(), ids, start, end)
	require.NoError(t, err)
	require.Equal(t, 1.5, stats[11].TotalActualCost)
	require.Equal(t, 0.25, stats[11].TodayActualCost)
	require.Equal(t, 2.0, stats[12].TotalActualCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

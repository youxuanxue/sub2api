//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryGetAllGroupUsageSummaryUsesRollupTail(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newUsageLogRepositoryWithSQL(nil, db)
	useGroupUsageRepositoryTestTimezone(t, "America/New_York")
	todayStart := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
	yesterdayStart := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`(?s)pg_catalog\.pg_class.*usage_dashboard_group_daily`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`(?s)usage_group_rollup_state.*usage_group_daily_rollups.*created_at >= state\.tail_start`).
		WithArgs(todayStart, yesterdayStart, "America/New_York", "2026-03-09", "2026-03-08").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "total_cost", "today_cost", "yesterday_cost"}).
			AddRow(int64(7), 12.5, 1.25, 2.5))

	result, err := repo.GetAllGroupUsageSummary(context.Background(), todayStart)
	require.NoError(t, err)
	require.Equal(t, int64(7), result[0].GroupID)
	require.InDelta(t, 12.5, result[0].TotalCost, 0.0000001)
	require.InDelta(t, 1.25, result[0].TodayCost, 0.0000001)
	require.InDelta(t, 2.5, result[0].YesterdayCost, 0.0000001)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageLogRepositoryGetAllGroupUsageSummaryUsesTKRollupWhenBackfilled(t *testing.T) {
	require.NoError(t, timezone.Init("UTC"))
	db, mock := newSQLMock(t)
	repo := newUsageLogRepositoryWithSQL(nil, db)
	todayStart := timezone.StartOfDay(timezone.Now())
	serverTodayDate := todayStart.Format("2006-01-02")
	serverYesterdayDate := todayStart.AddDate(0, 0, -1).Format("2006-01-02")

	mock.ExpectQuery(`(?s)pg_catalog\.pg_class.*usage_dashboard_group_daily`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM usage_dashboard_group_daily WHERE group_id = 0 AND bucket_date = DATE '1970-01-01'\)`).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`(?s)FROM usage_dashboard_group_daily.*bucket_date < \$1::date.*bucket_date = \$4::date`).
		WithArgs(serverTodayDate, todayStart, todayStart, serverYesterdayDate).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "total_cost", "today_cost", "yesterday_cost"}).
			AddRow(int64(9), 20.0, 3.0, 4.5))

	result, err := repo.GetAllGroupUsageSummary(context.Background(), todayStart)
	require.NoError(t, err)
	require.Equal(t, int64(9), result[0].GroupID)
	require.InDelta(t, 20.0, result[0].TotalCost, 0.0000001)
	require.InDelta(t, 3.0, result[0].TodayCost, 0.0000001)
	require.InDelta(t, 4.5, result[0].YesterdayCost, 0.0000001)
	require.NoError(t, mock.ExpectationsWereMet())
}

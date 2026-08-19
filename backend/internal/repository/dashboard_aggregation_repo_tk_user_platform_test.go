package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestBackfillUserPlatformDailyAllOnceUsesConfiguredReportingTimezone(t *testing.T) {
	require.NoError(t, timezone.Init("Asia/Shanghai"))
	t.Cleanup(func() { require.NoError(t, timezone.Init("UTC")) })

	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	mock.ExpectBegin()

	mock.ExpectQuery(`SELECT success_metrics_version, timezone_name[\s\S]*usage_dashboard_user_platform_rollup_state`).
		WillReturnRows(sqlmock.NewRows([]string{"success_metrics_version", "timezone_name"}).AddRow(0, ""))
	mock.ExpectQuery(`SELECT MIN\(created_at\), MAX\(created_at\) FROM usage_logs`).
		WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(
			time.Date(2026, 8, 1, 16, 30, 0, 0, time.UTC),
			time.Date(2026, 8, 1, 16, 30, 0, 0, time.UTC),
		))
	mock.ExpectExec(`DELETE FROM usage_dashboard_user_platform_daily`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)WITH.*AT TIME ZONE \$3.*INSERT INTO usage_dashboard_user_platform_daily`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "Asia/Shanghai").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE usage_dashboard_user_platform_rollup_state[\s\S]*success_metrics_version`).
		WithArgs(userPlatformSuccessMetricsVersion, "Asia/Shanghai").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repo := newDashboardAggregationRepositoryWithSQL(db)
	require.NoError(t, repo.BackfillUserPlatformDaily(context.Background()))
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBackfillUserPlatformDailyAllOnceMarksEmptyHistoryComplete(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	mock.ExpectBegin()

	mock.ExpectQuery(`SELECT success_metrics_version, timezone_name[\s\S]*usage_dashboard_user_platform_rollup_state`).
		WillReturnRows(sqlmock.NewRows([]string{"success_metrics_version", "timezone_name"}).AddRow(0, ""))
	mock.ExpectQuery(`SELECT MIN\(created_at\), MAX\(created_at\) FROM usage_logs`).
		WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(sql.NullTime{}, sql.NullTime{}))
	mock.ExpectExec(`UPDATE usage_dashboard_user_platform_rollup_state[\s\S]*success_metrics_version`).
		WithArgs(userPlatformSuccessMetricsVersion, "UTC").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	repo := newDashboardAggregationRepositoryWithSQL(db)
	require.NoError(t, repo.BackfillUserPlatformDaily(context.Background()))
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

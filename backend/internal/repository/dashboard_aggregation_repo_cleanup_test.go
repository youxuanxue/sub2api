//go:build unit

package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCleanupUsageLogs_PartitionedReclaimsStraddlingLegacy(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }
	todayStart := service.GroupUsageTodayStart(fixedNow)

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT EXISTS(
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = 'usage_logs'
		)
	`)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT c.relname`).
		WillReturnRows(sqlmock.NewRows([]string{"relname"}).AddRow("usage_logs_legacy"))
	mock.ExpectQuery(`SELECT n.nspname, c.relname`).
		WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname", "bound_expr", "upper_bound", "estimated_rows"}).
			AddRow("public", "usage_logs_legacy", "FOR VALUES FROM (MINVALUE) TO ('2026-08-10')", time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), int64(1000)))
	mock.ExpectQuery(`SELECT n.nspname, c.relname`).
		WillReturnRows(sqlmock.NewRows([]string{"nspname", "relname", "bound_expr", "lower_unbounded", "lower_bound"}).
			AddRow("public", "usage_logs_legacy", "FOR VALUES FROM (MINVALUE) TO ('2026-08-10')", true, nil))
	mock.ExpectQuery(`SELECT min\("created_at"\) FROM "public"\."usage_logs_legacy"`).
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)))
	mock.ExpectExec(`DELETE FROM "usage_logs_legacy"`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(`DELETE FROM "usage_logs_legacy"`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT closed_before::text, retained_from.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"closed_before", "retained_from", "timezone_name"}).
			AddRow(service.GroupUsageDate(todayStart), time.Unix(0, 0).UTC(), "Asia/Shanghai"))
	mock.ExpectCommit()

	err := repo.CleanupUsageLogs(context.Background(), cutoff)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteOldUsageLogRowsByID_RespectsMaxRows(t *testing.T) {
	db, mock := newSQLMock(t)
	cutoff := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)

	mock.ExpectExec("DELETE FROM \"usage_logs_legacy\"").
		WithArgs(cutoff, 5).
		WillReturnResult(sqlmock.NewResult(0, 5))

	deleted, err := deleteOldUsageLogRowsByID(
		context.Background(),
		db,
		"usage_logs_legacy",
		cutoff,
		usageLogsCleanupBatchSize,
		5,
	)
	require.NoError(t, err)
	require.Equal(t, int64(5), deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanupUsageLogs_NonPartitionedContinuesUntilShortBatch(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }
	todayStart := service.GroupUsageTodayStart(fixedNow)
	fullBatchAt := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	shortBatchAt := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("pg_partitioned_table").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`(?s)DELETE FROM usage_logs.*RETURNING created_at.*SELECT COUNT\(\*\) AS affected, MIN\(created_at\) AS earliest_deleted_at`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"affected", "earliest_deleted_at"}).
			AddRow(usageLogsCleanupBatchSize, fullBatchAt))
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).
		WithArgs(fullBatchAt, "Asia/Shanghai").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`(?s)DELETE FROM usage_logs.*RETURNING created_at.*SELECT COUNT\(\*\) AS affected, MIN\(created_at\) AS earliest_deleted_at`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"affected", "earliest_deleted_at"}).AddRow(1, shortBatchAt))
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).
		WithArgs(shortBatchAt, "Asia/Shanghai").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT closed_before::text, retained_from.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"closed_before", "retained_from", "timezone_name"}).
			AddRow(service.GroupUsageDate(todayStart), time.Unix(0, 0).UTC(), "Asia/Shanghai"))
	mock.ExpectCommit()

	require.NoError(t, repo.CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanupUsageLogs_NonPartitionedStopsAtPerRunCapAndSyncs(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	deletedAt := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }
	todayStart := service.GroupUsageTodayStart(fixedNow)

	mock.ExpectQuery("pg_partitioned_table").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	for i := 0; i < usageLogsCleanupMaxRowsPerRun/usageLogsCleanupBatchSize; i++ {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
		mock.ExpectQuery(`(?s)DELETE FROM usage_logs.*RETURNING created_at.*SELECT COUNT\(\*\) AS affected, MIN\(created_at\) AS earliest_deleted_at`).
			WithArgs(cutoff, usageLogsCleanupBatchSize).
			WillReturnRows(sqlmock.NewRows([]string{"affected", "earliest_deleted_at"}).
				AddRow(usageLogsCleanupBatchSize, deletedAt))
		mock.ExpectExec(`UPDATE usage_group_rollup_state`).
			WithArgs(deletedAt, "Asia/Shanghai").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT closed_before::text, retained_from.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"closed_before", "retained_from", "timezone_name"}).
			AddRow(service.GroupUsageDate(todayStart), time.Unix(0, 0).UTC(), "Asia/Shanghai"))
	mock.ExpectCommit()

	require.NoError(t, repo.CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanupUsageBillingDedupRespectsPerRunCap(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)

	for i := 0; i < usageBillingDedupCleanupMaxRowsPerRun/usageBillingDedupCleanupBatchSize; i++ {
		mock.ExpectExec("INSERT INTO usage_billing_dedup_archive").
			WithArgs(cutoff, usageBillingDedupCleanupBatchSize).
			WillReturnResult(sqlmock.NewResult(0, usageBillingDedupCleanupBatchSize))
	}

	require.NoError(t, repo.CleanupUsageBillingDedup(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanupUsageLogs_NonPartitionedUsesChunkedDelete(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }
	todayStart := service.GroupUsageTodayStart(fixedNow)
	deletedAt := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT EXISTS(
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = 'usage_logs'
		)
	`)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM usage_group_rollup_state.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery(`(?s)DELETE FROM usage_logs.*RETURNING created_at.*SELECT COUNT\(\*\) AS affected, MIN\(created_at\) AS earliest_deleted_at`).
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"affected", "earliest_deleted_at"}).AddRow(2, deletedAt))
	mock.ExpectExec(`UPDATE usage_group_rollup_state`).
		WithArgs(deletedAt, "Asia/Shanghai").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT closed_before::text, retained_from.*FOR UPDATE`).
		WillReturnRows(sqlmock.NewRows([]string{"closed_before", "retained_from", "timezone_name"}).
			AddRow(service.GroupUsageDate(todayStart), time.Unix(0, 0).UTC(), "Asia/Shanghai"))
	mock.ExpectCommit()

	err := repo.CleanupUsageLogs(context.Background(), cutoff)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

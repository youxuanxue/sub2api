//go:build unit

package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestCleanupUsageLogs_PartitionedReclaimsStraddlingLegacy(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT EXISTS(
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = 'usage_logs'
		)
	`)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	mock.ExpectQuery("FROM pg_inherits i").WillReturnRows(
		sqlmock.NewRows([]string{"nspname", "relname", "bound_expr", "upper_bound", "estimated_rows"}).
			AddRow("public", "usage_logs_legacy", "FOR VALUES FROM (MINVALUE) TO ('2026-08-10')", time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), int64(1000)),
	)

	mock.ExpectQuery("FROM pg_inherits i").WillReturnRows(
		sqlmock.NewRows([]string{"nspname", "relname", "bound_expr", "lower_unbounded", "lower_bound"}).
			AddRow("public", "usage_logs_legacy", "FOR VALUES FROM (MINVALUE) TO ('2026-08-10')", true, nil),
	)
	mock.ExpectQuery("SELECT min\\(\"created_at\"\\) FROM \"public\"\\.\"usage_logs_legacy\"").
		WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)))

	mock.ExpectExec("DELETE FROM \"usage_logs_legacy\"").
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("DELETE FROM \"usage_logs_legacy\"").
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnResult(sqlmock.NewResult(0, 0))

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

func TestCleanupUsageLogs_NonPartitionedRespectsPerRunCap(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("pg_partitioned_table").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	for i := 0; i < usageLogsCleanupMaxRowsPerRun/usageLogsCleanupBatchSize; i++ {
		mock.ExpectExec("DELETE FROM usage_logs").
			WithArgs(cutoff, usageLogsCleanupBatchSize).
			WillReturnResult(sqlmock.NewResult(0, usageLogsCleanupBatchSize))
	}

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
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT EXISTS(
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = 'usage_logs'
		)
	`)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	mock.ExpectExec("DELETE FROM usage_logs").
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnResult(sqlmock.NewResult(0, 2))

	err := repo.CleanupUsageLogs(context.Background(), cutoff)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

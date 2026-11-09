//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestPurgeExpiredUsageBillingDedup_DrainsUntilShortBatch(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	mock.ExpectExec(`(?s)FROM usage_billing_dedup\s+WHERE created_at < \$1.*DELETE FROM usage_billing_dedup\s+WHERE ctid IN`).
		WithArgs(cutoff, int64(usageBillingDedupPurgeBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, usageBillingDedupPurgeBatchSize))
	mock.ExpectExec(`DELETE FROM usage_billing_dedup\s+WHERE ctid IN`).
		WithArgs(cutoff, int64(usageBillingDedupPurgeBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 42))

	deleted, more, err := repo.PurgeExpiredUsageBillingDedup(context.Background(), cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(usageBillingDedupPurgeBatchSize+42), deleted)
	require.False(t, more)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPurgeExpiredUsageBillingDedup_StopsAtPerRunCapAndReportsBacklog(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	for i := 0; i < usageBillingDedupPurgeMaxRowsPerRun/usageBillingDedupPurgeBatchSize; i++ {
		mock.ExpectExec(`DELETE FROM usage_billing_dedup\s+WHERE ctid IN`).
			WithArgs(cutoff, int64(usageBillingDedupPurgeBatchSize)).
			WillReturnResult(sqlmock.NewResult(0, usageBillingDedupPurgeBatchSize))
	}

	deleted, more, err := repo.PurgeExpiredUsageBillingDedup(context.Background(), cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(usageBillingDedupPurgeMaxRowsPerRun), deleted)
	require.True(t, more, "a full final batch at the cap means older rows may remain")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanupUsageBillingDedupArchive_DeletesByPrimaryKey(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	mock.ExpectExec(`(?s)FROM usage_billing_dedup_archive\s+WHERE created_at < \$1\s+LIMIT \$2.*DELETE FROM usage_billing_dedup_archive a\s+USING victims v\s+WHERE a.request_id = v.request_id AND a.api_key_id = v.api_key_id`).
		WithArgs(cutoff, int64(usageBillingDedupPurgeBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, 3))

	deleted, more, err := repo.CleanupUsageBillingDedupArchive(context.Background(), cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(3), deleted)
	require.False(t, more)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPurgeExpiredUsageBillingDedup_ReturnsPartialCountOnError(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDashboardAggregationRepositoryWithSQL(db)
	cutoff := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	mock.ExpectExec(`DELETE FROM usage_billing_dedup\s+WHERE ctid IN`).
		WithArgs(cutoff, int64(usageBillingDedupPurgeBatchSize)).
		WillReturnResult(sqlmock.NewResult(0, usageBillingDedupPurgeBatchSize))
	mock.ExpectExec(`DELETE FROM usage_billing_dedup\s+WHERE ctid IN`).
		WithArgs(cutoff, int64(usageBillingDedupPurgeBatchSize)).
		WillReturnError(errors.New("canceling statement due to lock timeout"))

	deleted, _, err := repo.PurgeExpiredUsageBillingDedup(context.Background(), cutoff)
	require.ErrorContains(t, err, "lock timeout")
	require.Equal(t, int64(usageBillingDedupPurgeBatchSize), deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

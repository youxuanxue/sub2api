package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestManagedBatchCASOnlyUpdatesMatchingOwnerAndVersion(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	r := NewManagedBatchRepository(db)
	retry := time.Unix(10, 0).UTC()
	code, message := "retryable", "temporary"
	mock.ExpectExec(regexp.QuoteMeta("UPDATE batches SET status=$1")).
		WithArgs("retry", &retry, &code, &message, "batch-1", int64(7), int64(9), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	ok, err := r.CompareAndSwapBatchStatus(context.Background(), 7, 9, "batch-1", 2, "retry", &retry, &code, &message)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagedBatchClaimItemsUsesLeaseAndSkipsLockedRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	r := NewManagedBatchRepository(db)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("WITH picked AS (")).
		WithArgs("batch-1", "worker-a", 2, int64(60)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "batch_id", "custom_id", "status", "request", "attempt_count", "lease_owner", "lease_expires_at"}).
			AddRow(int64(3), "batch-1", "row-3", "running", []byte(`{"method":"POST"}`), 1, "worker-a", time.Now()))
	mock.ExpectCommit()
	items, err := r.ClaimItems(context.Background(), "batch-1", "worker-a", 2, time.Minute)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int64(3), items[0].ID)
	require.Equal(t, "POST", items[0].Request["method"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagedBatchCompleteItemRequiresLeaseOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	r := NewManagedBatchRepository(db)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE batch_items SET status=$1")).
		WithArgs("succeeded", []byte(`{"ok":true}`), []byte(`null`), int64(4), int64(6), false, int64(3), "worker-a").
		WillReturnResult(sqlmock.NewResult(0, 0))
	ok, err := r.CompleteItem(context.Background(), 3, "worker-a", "succeeded", map[string]any{"ok": true}, nil, 4, 6, false)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagedBatchUpdateProgressPublishesPublicCounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	r := NewManagedBatchRepository(db)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE batches SET request_counts=$1::jsonb")).
		WithArgs(`{"total":4,"completed":4,"failed":2}`, "batch-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	err = r.UpdateBatchProgress(context.Background(), "batch-1", ManagedBatchProgress{Total: 4, Succeeded: 1, Failed: 1, Cancelled: 1, Unknown: 1})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagedBatchResetRetryRequeuesWithOwnerPayload(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	r := NewManagedBatchRepository(db)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("UPDATE batches SET status='validating'")).
		WithArgs("batch-1", int64(7), int64(9), int64(3), "batch-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE batch_items SET status='pending'")).
		WithArgs("batch-1").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("jsonb_build_object('batch_id',$1,'user_id',$2,'api_key_id',$3)")).
		WithArgs("batch-1", int64(7), int64(9), "batch.retry:batch-1:4").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	ok, err := r.ResetBatchForRetry(context.Background(), 7, 9, "batch-1", 3)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagedBatchListCursorsUseCreatedAtAndID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	r := NewManagedBatchRepository(db)
	mock.ExpectQuery(regexp.QuoteMeta("AND (created_at,id) < (SELECT created_at,id FROM batch_files")).
		WithArgs(int64(7), int64(9), "file-cursor", 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "purpose", "filename", "storage_ref", "byte_size", "sha256", "status", "user_id", "api_key_id", "expires_at", "created_at"}))
	_, _, err = r.ListFilesForOwner(context.Background(), 7, 9, "", "file-cursor", 1)
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("AND (created_at,id) < (SELECT created_at,id FROM batches")).
		WithArgs(int64(7), int64(9), "batch-cursor", 2).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "api_key_id", "input_file_id", "endpoint", "completion_window", "status", "request_counts", "metadata", "in_progress_at", "finalizing_at", "cancelling_at", "completed_at", "cancelled_at", "expired_at", "failed_at", "deadline_at", "version", "created_at"}))
	_, _, err = r.ListBatchesForOwner(context.Background(), 7, 9, "batch-cursor", 1)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

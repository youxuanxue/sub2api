//go:build integration

package repository

import (
	"context"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type batchRetryObjects map[string]string

func (s batchRetryObjects) Upload(_ context.Context, key string, body io.Reader, _ int64, _ string) (int64, error) {
	data, err := io.ReadAll(body)
	s[key] = string(data)
	return int64(len(data)), err
}

func TestManagedBatchRetryRecoveryAndDownload(t *testing.T) {
	ctx := context.Background()
	repo := NewManagedBatchRepository(integrationDB)
	id := uuid.NewString()
	file := "file_" + id
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := integrationDB.ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	exec("INSERT INTO batch_files(id,user_id,api_key_id,purpose,filename,storage_ref) VALUES ($1,7,9,'batch','in','test')", file)
	exec("INSERT INTO batches(id,user_id,api_key_id,input_file_id,endpoint,status,deadline_at) VALUES ($1,7,9,$2,'/v1/responses','finalizing',now()+interval '1 day')", id, file)
	exec("INSERT INTO batch_items(batch_id,custom_id,request,status,lease_owner,lease_expires_at) VALUES ($1,'lost','{}','running','old',now()-interval '1 minute'),($1,'live','{}','running','active',now()+interval '1 minute')", id)
	progress, err := repo.CountBatchItems(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 1, progress.Unknown)
	require.Equal(t, 1, progress.Running)
	// Exercise a retry with one previously successful and one failed sample.
	exec("DELETE FROM batch_items WHERE batch_id=$1", id)
	exec(`INSERT INTO batch_items(batch_id,custom_id,request,status,response) VALUES ($1,'ok','{}','succeeded','{"status_code":200,"body":{"id":"first"}}'),($1,'bad','{}','failed',NULL)`, id)
	store := batchRetryObjects{}
	finalizer := ManagedBatchResultFinalizer{Repo: repo, Store: store}
	require.NoError(t, finalizer.Finalize(ctx, id))
	first, err := repo.getBatchForResult(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, first.ErrorFileID)
	exec("UPDATE batches SET status='failed' WHERE id=$1", id)
	reopened, err := repo.ResetBatchForRetry(ctx, 7, 9, id, first.Version)
	require.NoError(t, err)
	require.True(t, reopened)
	exec("UPDATE batches SET status='finalizing' WHERE id=$1", id)
	exec(`UPDATE batch_items SET status='succeeded',response='{"status_code":200,"body":{"id":"ok"}}' WHERE batch_id=$1 AND status='pending'`, id)
	require.NoError(t, finalizer.Finalize(ctx, id))
	current, err := repo.getBatchForResult(ctx, id)
	require.NoError(t, err)
	require.Nil(t, current.ErrorFileID)
	require.NotNil(t, current.OutputFileID)
	require.NotNil(t, first.OutputFileID)
	require.NotEqual(t, *first.OutputFileID, *current.OutputFileID)
	require.Len(t, store, 3)
	require.NoError(t, finalizer.Finalize(ctx, id))
	require.Len(t, store, 3)
}

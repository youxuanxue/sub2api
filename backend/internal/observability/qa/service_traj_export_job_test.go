//go:build unit

package qa

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/qaexportjob"
	"github.com/stretchr/testify/require"
)

// trajBlobFixture is the smallest /v1/messages evidence blob that projects to a
// non-empty trajectory (mirrors the download/projection tests).
func trajBlobFixture() map[string]any {
	return map[string]any{
		"request":  map[string]any{"path": "/v1/messages", "body": map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}},
		"response": map[string]any{"status_code": 200, "headers": map[string]any{}, "body": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}},
		"stream":   map[string]any{"chunks": []any{}},
	}
}

func waitExport(t *testing.T, svc *Service, userID int64, jobID string) ExportJob {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 400; i++ {
		j, ok := svc.GetExportJob(ctx, userID, jobID)
		require.True(t, ok, "job %s should be queryable", jobID)
		if j.Status == ExportJobDone || j.Status == ExportJobFailed {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("export %s did not reach a terminal state", jobID)
	return ExportJob{}
}

// exportStorageKey lays out user/key-first keys so the download ownership
// prefix check holds and each artifact is bound to one API key.
func TestExportStorageKeyLayout(t *testing.T) {
	key := exportStorageKey(7, 9)
	require.True(t, strings.HasPrefix(key, "traj-exports/7/9/manual/"), key)
	require.True(t, strings.HasSuffix(key, ".zip"))
}

// A finished export must remain queryable after a "redeploy" (a fresh Service
// over the same DB) — the persistence fix for the orphaned-download bug.
func TestUS044_EnqueueExport_RejectsMissingAPIKey(t *testing.T) {
	svc, _, _ := newQAExportTestService(t)
	job, err := svc.EnqueueExport(context.Background(), 7, ExportFilter{})
	require.ErrorIs(t, err, ErrExportAPIKeyNeeded)
	require.Empty(t, job.ID)
}

func TestUS044_EnqueueExport_ClampsTo24HoursAndSurvivesRestart(t *testing.T) {
	svc, client, store := newQAExportTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{
		requestID: "enq-current", userID: 7, apiKeyID: 3, createdAt: now, synthSession: "m0-ENQ",
	}, trajBlobFixture())
	mustInsertQARecordWithBlob(t, ctx, client, store, qaRecordBuilder{
		requestID: "enq-too-old", userID: 7, apiKeyID: 3, createdAt: now.Add(-30 * time.Hour), synthSession: "m0-ENQ",
	}, trajBlobFixture())

	key3 := int64(3)
	job, err := svc.EnqueueExport(ctx, 7, ExportFilter{
		APIKeyID: &key3,
		Since:    now.Add(-365 * 24 * time.Hour),
		Until:    now.Add(24 * time.Hour),
		Format:   "v2",
	})
	require.NoError(t, err)
	require.NotEmpty(t, job.ID)

	done := waitExport(t, svc, 7, job.ID)
	require.Equal(t, ExportJobDone, done.Status, "err=%s", done.Error)
	require.NotEmpty(t, done.StorageKey)
	require.Equal(t, 1, done.RecordCount, "service must clamp caller window to the last 24 hours")

	// Simulate a redeploy: a new Service over the same client/store.
	svc2 := NewServiceForTest(client, store)
	got, ok := svc2.GetExportJob(ctx, 7, job.ID)
	require.True(t, ok, "finished job survives restart")
	require.Equal(t, ExportJobDone, got.Status)
	require.NotEmpty(t, got.DownloadURL, "download URL recomputed on read")

	// A done job must NOT be touched by the startup orphan reconciler.
	require.NoError(t, svc2.ReconcileOrphanedExports(ctx))
	again, _ := svc2.GetExportJob(ctx, 7, job.ID)
	require.Equal(t, ExportJobDone, again.Status)
}

func TestReconcileOrphanedExports_FailsOnlyInFlight(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	ctx := context.Background()
	mk := func(jobID, status string) {
		_, err := client.QAExportJob.Create().
			SetJobID(jobID).SetUserID(7).SetStatus(status).Save(ctx)
		require.NoError(t, err)
	}
	mk("pend", string(ExportJobPending))
	mk("run", string(ExportJobRunning))
	mk("done", string(ExportJobDone))

	require.NoError(t, svc.ReconcileOrphanedExports(ctx))

	for _, jobID := range []string{"pend", "run"} {
		m, err := client.QAExportJob.Query().Where(qaexportjob.JobIDEQ(jobID)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, string(ExportJobFailed), m.Status)
		require.NotNil(t, m.Error)
		require.Equal(t, exportErrInterrupted, *m.Error)
	}
	doneRow, err := client.QAExportJob.Query().Where(qaexportjob.JobIDEQ("done")).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, string(ExportJobDone), doneRow.Status)
	require.Nil(t, doneRow.Error)
}

func TestListExports_OwnerIsolationAndKeyScope(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	ctx := context.Background()
	future := time.Now().UTC().Add(time.Hour)
	mk := func(jobID string, userID, apiKeyID int64) {
		_, err := client.QAExportJob.Create().
			SetJobID(jobID).SetUserID(userID).SetAPIKeyID(apiKeyID).
			SetStatus(string(ExportJobDone)).
			SetStorageKey("traj-exports/x/" + jobID + ".zip").SetRecordCount(1).
			SetExpiresAt(future).Save(ctx)
		require.NoError(t, err)
	}
	mk("u7k1", 7, 1)
	mk("u7k2", 7, 2)
	mk("u8k1", 8, 1)

	all7, err := svc.ListExports(ctx, 7, nil)
	require.NoError(t, err)
	require.Len(t, all7, 2, "only user 7's exports")
	for _, j := range all7 {
		require.Equal(t, int64(7), j.UserID)
	}

	key1 := int64(1)
	scoped, err := svc.ListExports(ctx, 7, &key1)
	require.NoError(t, err)
	require.Len(t, scoped, 1)
	require.Equal(t, "u7k1", scoped[0].ID)

	all8, err := svc.ListExports(ctx, 8, nil)
	require.NoError(t, err)
	require.Len(t, all8, 1)
	require.Equal(t, "u8k1", all8[0].ID)
}

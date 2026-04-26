package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/trajectory"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func TestOpsServiceRecordErrorBatch_SanitizesAndBatches(t *testing.T) {
	t.Parallel()

	var captured []*OpsInsertErrorLogInput
	repo := &opsRepoMock{
		BatchInsertErrorLogsFn: func(ctx context.Context, inputs []*OpsInsertErrorLogInput) (int64, error) {
			captured = append(captured, inputs...)
			return int64(len(inputs)), nil
		},
	}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	msg := " upstream failed: https://example.com?access_token=secret-value "
	detail := `{"authorization":"Bearer secret-token"}`
	entries := []*OpsInsertErrorLogInput{
		{
			ErrorBody:            `{"error":"bad","access_token":"secret"}`,
			UpstreamStatusCode:   intPtr(-10),
			UpstreamErrorMessage: strPtr(msg),
			UpstreamErrorDetail:  strPtr(detail),
			UpstreamErrors: []*OpsUpstreamErrorEvent{
				{
					AccountID:           -2,
					UpstreamStatusCode:  429,
					Message:             " token leaked ",
					Detail:              `{"refresh_token":"secret"}`,
					UpstreamRequestBody: `{"api_key":"secret","messages":[{"role":"user","content":"hello"}]}`,
				},
			},
		},
		{
			ErrorPhase: "upstream",
			ErrorType:  "upstream_error",
			CreatedAt:  time.Now().UTC(),
		},
	}

	require.NoError(t, svc.RecordErrorBatch(context.Background(), entries))
	require.Len(t, captured, 2)

	first := captured[0]
	require.Equal(t, "internal", first.ErrorPhase)
	require.Equal(t, "api_error", first.ErrorType)
	require.Nil(t, first.UpstreamStatusCode)
	require.NotNil(t, first.UpstreamErrorMessage)
	require.NotContains(t, *first.UpstreamErrorMessage, "secret-value")
	require.Contains(t, *first.UpstreamErrorMessage, "access_token=***")
	require.NotNil(t, first.UpstreamErrorDetail)
	require.NotContains(t, *first.UpstreamErrorDetail, "secret-token")
	require.NotContains(t, first.ErrorBody, "secret")
	require.Nil(t, first.UpstreamErrors)
	require.NotNil(t, first.UpstreamErrorsJSON)
	require.NotContains(t, *first.UpstreamErrorsJSON, "secret")
	require.Contains(t, *first.UpstreamErrorsJSON, "[REDACTED]")

	second := captured[1]
	require.Equal(t, "upstream", second.ErrorPhase)
	require.Equal(t, "upstream_error", second.ErrorType)
	require.False(t, second.CreatedAt.IsZero())
}

func TestOpsServiceRecordErrorBatch_FallsBackToSingleInsert(t *testing.T) {
	t.Parallel()

	var (
		batchCalls  int
		singleCalls int
	)
	repo := &opsRepoMock{
		BatchInsertErrorLogsFn: func(ctx context.Context, inputs []*OpsInsertErrorLogInput) (int64, error) {
			batchCalls++
			return 0, errors.New("batch failed")
		},
		InsertErrorLogFn: func(ctx context.Context, input *OpsInsertErrorLogInput) (int64, error) {
			singleCalls++
			return int64(singleCalls), nil
		},
	}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	err := svc.RecordErrorBatch(context.Background(), []*OpsInsertErrorLogInput{
		{ErrorMessage: "first"},
		{ErrorMessage: "second"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, batchCalls)
	require.Equal(t, 2, singleCalls)
}

func TestOpsServiceRecordError_FallsBackWhenRepoUnavailable(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	before := trajectory.DLQWrites()
	svc := NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	err := svc.RecordError(context.Background(), &OpsInsertErrorLogInput{
		RequestID:    "req_repo_unavailable",
		TrajectoryID: "traj_repo_unavailable",
		ErrorPhase:   "upstream",
		ErrorType:    "upstream_error",
		ErrorMessage: "failed",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, before+1, trajectory.DLQWrites())
	payload := readOpsFallbackPayload(t, filepath.Join(os.Getenv("DATA_DIR"), "ops_dlq", "req_repo_unavailable.json.zst"))
	require.Equal(t, "ops_error_fallback", payload["kind"])
	require.Equal(t, "ops_repo_unavailable", payload["fallback_for"])
	entryPayload, ok := payload["entry"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "req_repo_unavailable", entryPayload["RequestID"])
	require.Equal(t, "traj_repo_unavailable", entryPayload["TrajectoryID"])
}

func TestOpsServiceRecordError_FallsBackWhenInsertFails(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	before := trajectory.DLQWrites()
	repo := &opsRepoMock{
		InsertErrorLogFn: func(ctx context.Context, input *OpsInsertErrorLogInput) (int64, error) {
			return 0, errors.New("insert failed")
		},
	}
	svc := NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	err := svc.RecordError(context.Background(), &OpsInsertErrorLogInput{
		RequestID:    "req_insert_failed",
		TrajectoryID: "traj_insert_failed",
		ErrorPhase:   "upstream",
		ErrorType:    "upstream_error",
		ErrorMessage: "failed",
	}, nil)
	require.Error(t, err)
	require.Equal(t, before+1, trajectory.DLQWrites())
	payload := readOpsFallbackPayload(t, filepath.Join(os.Getenv("DATA_DIR"), "ops_dlq", "req_insert_failed.json.zst"))
	require.Equal(t, "ops_error_fallback", payload["kind"])
	require.Equal(t, "ops_insert_failed", payload["fallback_for"])
	entryPayload, ok := payload["entry"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "req_insert_failed", entryPayload["RequestID"])
	require.Equal(t, "traj_insert_failed", entryPayload["TrajectoryID"])
}

func readOpsFallbackPayload(t *testing.T, path string) map[string]any {
	t.Helper()

	// #nosec G304,G703 -- path is a test-controlled temp file created within this test.
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	dec, err := zstd.NewReader(nil)
	require.NoError(t, err)
	decoded, err := dec.DecodeAll(raw, nil)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(decoded, &payload))
	return payload
}

func strPtr(v string) *string {
	return &v
}

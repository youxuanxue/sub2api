package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qaCaptureHealthStub struct {
	status string
	result string
	err    error
}

func (s qaCaptureHealthStub) QACaptureHealth() (string, string, error) {
	return s.status, s.result, s.err
}

func TestOpsMetricsCollectorMirrorsQACaptureHealth(t *testing.T) {
	var heartbeat *OpsUpsertJobHeartbeatInput
	repo := &opsRepoMock{
		UpsertJobHeartbeatFn: func(_ context.Context, input *OpsUpsertJobHeartbeatInput) error {
			heartbeat = input
			return nil
		},
	}
	collector := &OpsMetricsCollector{
		opsRepo: repo,
		qaCaptureHealth: qaCaptureHealthStub{
			status: "failed",
			result: `{"status":"failed","reason":"persist_failed"}`,
		},
	}
	runAt := time.Date(2026, 8, 15, 10, 1, 0, 0, time.UTC)

	collector.mirrorQACaptureHealth(context.Background(), runAt)

	require.NotNil(t, heartbeat)
	require.Equal(t, "qa_capture", heartbeat.JobName)
	require.Equal(t, runAt, *heartbeat.LastRunAt)
	require.Equal(t, runAt, *heartbeat.LastErrorAt)
	require.Equal(t, "failed", *heartbeat.LastError)
	require.Equal(t, `{"status":"failed","reason":"persist_failed"}`, *heartbeat.LastResult)
}

func TestOpsMetricsCollectorMirrorsHealthyCaptureAsSuccess(t *testing.T) {
	var heartbeat *OpsUpsertJobHeartbeatInput
	repo := &opsRepoMock{
		UpsertJobHeartbeatFn: func(_ context.Context, input *OpsUpsertJobHeartbeatInput) error {
			heartbeat = input
			return nil
		},
	}
	collector := &OpsMetricsCollector{
		opsRepo: repo,
		qaCaptureHealth: qaCaptureHealthStub{
			status: "healthy",
			result: `{"status":"healthy"}`,
		},
	}
	runAt := time.Date(2026, 8, 15, 10, 1, 0, 0, time.UTC)

	collector.mirrorQACaptureHealth(context.Background(), runAt)

	require.NotNil(t, heartbeat)
	require.Equal(t, runAt, *heartbeat.LastSuccessAt)
	require.Nil(t, heartbeat.LastErrorAt)
}

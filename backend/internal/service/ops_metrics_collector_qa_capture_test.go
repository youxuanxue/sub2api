package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/captureledger"
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

// A degraded ledger is the channel unknown-format drift and evidence DLQ report
// through, so it has to land on LastErrorAt: ops_health_score only counts a
// heartbeat as failed when LastErrorAt is newer than LastSuccessAt, and nothing
// alerts on LastResult.
func TestOpsMetricsCollectorMirrorsDegradedCaptureAsError(t *testing.T) {
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
			status: "degraded",
			result: `{"status":"degraded","redaction":{"unknown_format_drift":true}}`,
		},
	}
	runAt := time.Date(2026, 8, 15, 10, 1, 0, 0, time.UTC)

	collector.mirrorQACaptureHealth(context.Background(), runAt)

	require.NotNil(t, heartbeat)
	require.Equal(t, runAt, *heartbeat.LastErrorAt)
	require.Equal(t, "degraded", *heartbeat.LastError)
	require.Nil(t, heartbeat.LastSuccessAt)

	// The drift evidence still reaches LastResult for display.
	require.Contains(t, *heartbeat.LastResult, `"unknown_format_drift":true`)
}

// An unrecognized status keeps the benign handling rather than alerting, so a
// future ledger status cannot turn every run into a false failure.
func TestOpsMetricsCollectorMirrorsUnknownStatusAsSuccess(t *testing.T) {
	var heartbeat *OpsUpsertJobHeartbeatInput
	repo := &opsRepoMock{
		UpsertJobHeartbeatFn: func(_ context.Context, input *OpsUpsertJobHeartbeatInput) error {
			heartbeat = input
			return nil
		},
	}
	collector := &OpsMetricsCollector{
		opsRepo:         repo,
		qaCaptureHealth: qaCaptureHealthStub{status: "", result: ""},
	}
	runAt := time.Date(2026, 8, 15, 10, 1, 0, 0, time.UTC)

	collector.mirrorQACaptureHealth(context.Background(), runAt)

	require.NotNil(t, heartbeat)
	require.Equal(t, runAt, *heartbeat.LastSuccessAt)
	require.Nil(t, heartbeat.LastErrorAt)
	require.Nil(t, heartbeat.LastResult)
}

// The literals above exist because QACaptureHealthSource reports status as a
// plain string; pin them to the ledger's own constants so renaming one side
// fails here instead of silently disabling the mirror.
func TestQACaptureStatusLiteralsMatchLedger(t *testing.T) {
	require.Equal(t, string(captureledger.HealthFailed), qaCaptureStatusFailed)
	require.Equal(t, string(captureledger.HealthDegraded), qaCaptureStatusDegraded)
	require.True(t, qaCaptureStatusNeedsAttention(string(captureledger.HealthFailed)))
	require.True(t, qaCaptureStatusNeedsAttention(string(captureledger.HealthDegraded)))
	require.False(t, qaCaptureStatusNeedsAttention(string(captureledger.HealthHealthy)))
}

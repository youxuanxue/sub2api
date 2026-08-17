//go:build unit

package qa

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/qarecord"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/captureledger"
	"github.com/stretchr/testify/require"
)

func TestSubmitUsesTrustedServerCaptureTime(t *testing.T) {
	svc, client, _ := newQAExportTestService(t)
	callerTime := time.Date(2026, 8, 15, 6, 42, 0, 0, time.UTC)
	serverNow := time.Date(2026, 8, 15, 9, 42, 0, 0, time.UTC)
	svc.now = func() time.Time { return serverNow }

	svc.Submit(CaptureInput{
		RequestID:  "trusted-capture-time",
		UserID:     7,
		APIKeyID:   1,
		Platform:   "anthropic",
		StatusCode: 200,
		CreatedAt:  callerTime,
	})

	record, err := client.QARecord.Query().
		Where(qarecord.RequestIDEQ("trusted-capture-time")).
		Only(context.Background())
	require.NoError(t, err)
	require.Equal(t, serverNow, record.CreatedAt)
}

func TestSubmitPersistsCaptureLedgerTransitions(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 40, 0, 0, time.UTC)
	svc, _, _ := newQAExportTestService(t)
	svc.now = func() time.Time { return now }
	ledger, err := captureledger.Open(t.TempDir(), "runtime-a", now.Add(-time.Minute), func() time.Time { return now })
	require.NoError(t, err)
	svc.captureLedger = ledger

	svc.Submit(CaptureInput{
		RequestID:  "ledger-persisted",
		UserID:     7,
		APIKeyID:   1,
		Platform:   "anthropic",
		StatusCode: 200,
	})

	receipt := readRuntimeReceipt(t, ledger.Root(), "runtime-a")
	counters := receipt.Hours[now.Format("20060102T15")]
	require.NotNil(t, counters)
	require.EqualValues(t, 0, counters.Pending)
	require.EqualValues(t, 0, counters.Inflight)
	require.EqualValues(t, 1, counters.Persisted)
}

func TestSubmitRecordsDatabasePersistFailureInLedger(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 40, 0, 0, time.UTC)
	svc, client, _ := newQAExportTestService(t)
	svc.now = func() time.Time { return now }
	ledger, err := captureledger.Open(t.TempDir(), "runtime-a", now.Add(-time.Minute), func() time.Time { return now })
	require.NoError(t, err)
	svc.captureLedger = ledger
	require.NoError(t, client.Close())

	svc.Submit(CaptureInput{
		RequestID:  "ledger-persist-failed",
		UserID:     7,
		APIKeyID:   1,
		Platform:   "anthropic",
		StatusCode: 200,
	})

	health, err := ledger.Health()
	require.NoError(t, err)
	require.Equal(t, captureledger.HealthFailed, health.Status)
	require.Equal(t, 1, health.UnresolvedFailures)
}

func TestSubmitMirrorsDLQCaptureAsDegradedLedgerHealth(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 40, 0, 0, time.UTC)
	svc, _, _ := newQAExportTestService(t)
	svc.now = func() time.Time { return now }
	svc.store = dlqOnlyBlobStore{}
	svc.dlqDir = filepath.Join(t.TempDir(), "qa_dlq")
	ledger, err := captureledger.Open(t.TempDir(), "runtime-a", now.Add(-time.Minute), func() time.Time { return now })
	require.NoError(t, err)
	svc.captureLedger = ledger

	svc.Submit(CaptureInput{
		RequestID:     "ledger-degraded-dlq",
		UserID:        7,
		APIKeyID:      1,
		Platform:      "anthropic",
		StatusCode:    200,
		CaptureStatus: captureStatusCaptured,
	})

	health, err := ledger.Health()
	require.NoError(t, err)
	require.Equal(t, captureledger.HealthDegraded, health.Status)
	require.Equal(t, "evidence_dlq", health.Reason)
}

func TestCaptureLedgerTickPublishesZeroRecordHourSeal(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)
	root := t.TempDir()
	ledger, err := captureledger.Open(root, "runtime-a", now, func() time.Time { return now })
	require.NoError(t, err)
	svc := &Service{
		now:           func() time.Time { return now },
		captureLedger: ledger,
	}

	now = time.Date(2026, 8, 15, 10, 1, 0, 0, time.UTC)
	svc.captureLedgerTick()

	seal, err := captureledger.ValidateHourSeal(root, time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC), now, captureledger.DefaultFreshness)
	require.NoError(t, err)
	require.Equal(t, []string{"runtime-a"}, seal.RuntimeIDs)
}

func TestQACaptureHealthReturnsLedgerMirrorPayload(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)
	ledger, err := captureledger.Open(t.TempDir(), "runtime-a", now, func() time.Time { return now })
	require.NoError(t, err)
	svc := &Service{captureLedger: ledger}

	status, result, err := svc.QACaptureHealth()
	require.NoError(t, err)
	require.Equal(t, "healthy", status)
	var payload captureledger.Health
	require.NoError(t, json.Unmarshal([]byte(result), &payload))
	require.Equal(t, captureledger.HealthHealthy, payload.Status)
}

func TestQACaptureHealthFailsClosedWhenLedgerDidNotOpen(t *testing.T) {
	svc := &Service{ledgerInitErr: errors.New("secret filesystem detail")}

	status, result, err := svc.QACaptureHealth()
	require.EqualError(t, err, "qa capture ledger unavailable")
	require.Equal(t, "failed", status)
	require.JSONEq(t, `{"status":"failed","reason":"ledger_unavailable"}`, result)
}

func readRuntimeReceipt(t *testing.T, root, runtimeID string) captureledger.RuntimeReceipt {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "runtimes", runtimeID+".json"))
	require.NoError(t, err)
	var receipt captureledger.RuntimeReceipt
	require.NoError(t, json.Unmarshal(body, &receipt))
	return receipt
}

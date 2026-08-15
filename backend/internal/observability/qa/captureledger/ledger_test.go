//go:build unit

package captureledger

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHourSealRequiresPersistedCaptureTransitions(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 40, 0, 0, time.UTC)
	ledger, err := Open(t.TempDir(), "runtime-a", now.Add(-10*time.Minute), func() time.Time { return now })
	require.NoError(t, err)

	identity := CaptureIdentity{RequestID: "req-1", CapturedAt: now}
	require.NoError(t, ledger.Begin(identity))
	now = time.Date(2026, 8, 15, 10, 16, 0, 0, time.UTC)
	_, err = ledger.SealHour(identity.SourceHour())
	require.ErrorIs(t, err, ErrHourUnsealed)

	require.NoError(t, ledger.Start(identity))
	require.NoError(t, ledger.Complete(identity, OutcomePersisted))
	require.NoError(t, ledger.Snapshot())
	seal, err := ledger.SealHour(identity.SourceHour())
	require.NoError(t, err)
	require.Equal(t, identity.SourceHour(), seal.SourceHour)
	require.Contains(t, seal.RuntimeIDs, "runtime-a")

	validated, err := ValidateHourSeal(ledger.Root(), identity.SourceHour(), now, DefaultFreshness)
	require.NoError(t, err)
	require.Equal(t, seal.StateDigest, validated.StateDigest)
}

func TestPersistFailureRemainsUnresolvedAfterLaterSuccess(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 40, 0, 0, time.UTC)
	ledger, err := Open(t.TempDir(), "runtime-a", now.Add(-10*time.Minute), func() time.Time { return now })
	require.NoError(t, err)

	failed := CaptureIdentity{RequestID: "req-failed", CapturedAt: now}
	require.NoError(t, ledger.Begin(failed))
	require.NoError(t, ledger.Start(failed))
	require.NoError(t, ledger.Complete(failed, OutcomePersistFailed))

	succeeded := CaptureIdentity{RequestID: "req-ok", CapturedAt: now.Add(time.Minute)}
	require.NoError(t, ledger.Begin(succeeded))
	require.NoError(t, ledger.Start(succeeded))
	require.NoError(t, ledger.Complete(succeeded, OutcomePersisted))

	now = time.Date(2026, 8, 15, 10, 16, 0, 0, time.UTC)
	require.NoError(t, ledger.Snapshot())
	_, err = ledger.SealHour(failed.SourceHour())
	require.ErrorIs(t, err, ErrHourUnsealed)

	health, err := ledger.Health()
	require.NoError(t, err)
	require.Equal(t, HealthFailed, health.Status)
	require.Equal(t, 1, health.UnresolvedFailures)

	require.NoError(t, ledger.Recover(failed))
	seal, err := ledger.SealHour(failed.SourceHour())
	require.NoError(t, err)
	require.NotEmpty(t, seal.StateDigest)
}

func TestLedgerWriteFailureStaysUnsealable(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 40, 0, 0, time.UTC)
	ledger, err := Open(t.TempDir(), "runtime-a", now.Add(-10*time.Minute), func() time.Time { return now })
	require.NoError(t, err)

	realWrite := ledger.writeAtomic
	failedOnce := false
	ledger.writeAtomic = func(path string, body []byte) error {
		if !failedOnce {
			failedOnce = true
			return errors.New("shared volume unavailable")
		}
		return realWrite(path, body)
	}

	identity := CaptureIdentity{RequestID: "req-ledger-failure", CapturedAt: now}
	require.Error(t, ledger.Begin(identity))
	require.NoError(t, ledger.Start(identity))
	require.NoError(t, ledger.Complete(identity, OutcomePersisted))

	now = time.Date(2026, 8, 15, 10, 16, 0, 0, time.UTC)
	require.NoError(t, ledger.Snapshot())
	_, err = ledger.SealHour(identity.SourceHour())
	require.ErrorIs(t, err, ErrHourUnsealed)

	health, err := ledger.Health()
	require.NoError(t, err)
	require.Equal(t, HealthFailed, health.Status)
	require.Equal(t, "ledger_write_failed", health.Reason)
}

func TestHealthFailsWhenCaptureHasNoProgressForFiveMinutes(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	ledger, err := Open(t.TempDir(), "runtime-a", now, func() time.Time { return now })
	require.NoError(t, err)
	require.NoError(t, ledger.Begin(CaptureIdentity{RequestID: "req-stalled", CapturedAt: now}))

	now = now.Add(4 * time.Minute)
	health, err := ledger.Health()
	require.NoError(t, err)
	require.Equal(t, HealthHealthy, health.Status)

	now = now.Add(2 * time.Minute)
	health, err = ledger.Health()
	require.NoError(t, err)
	require.Equal(t, HealthFailed, health.Status)
	require.Equal(t, "capture_stalled", health.Reason)
}

func TestRecoveredFailureNeedsHealthyWindowBeforeHealthClears(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	ledger, err := Open(t.TempDir(), "runtime-a", now, func() time.Time { return now })
	require.NoError(t, err)
	identity := CaptureIdentity{RequestID: "req-recovered", CapturedAt: now}
	require.NoError(t, ledger.Begin(identity))
	require.NoError(t, ledger.Start(identity))
	require.NoError(t, ledger.Complete(identity, OutcomePersistFailed))

	now = now.Add(time.Minute)
	require.NoError(t, ledger.Recover(identity))
	health, err := ledger.Health()
	require.NoError(t, err)
	require.Equal(t, HealthFailed, health.Status)
	require.Equal(t, "recovery_observation", health.Reason)

	now = now.Add(DefaultFreshness)
	health, err = ledger.Health()
	require.NoError(t, err)
	require.Equal(t, HealthHealthy, health.Status)
}

func TestOverlappingRuntimeWithCleanDrainCanSeal(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)
	root := t.TempDir()
	runtimeA, err := Open(root, "runtime-a", now, func() time.Time { return now })
	require.NoError(t, err)

	now = time.Date(2026, 8, 15, 9, 49, 0, 0, time.UTC)
	require.NoError(t, runtimeA.Snapshot())
	now = time.Date(2026, 8, 15, 9, 50, 0, 0, time.UTC)
	runtimeB, err := Open(root, "runtime-b", now, func() time.Time { return now })
	require.NoError(t, err)
	now = time.Date(2026, 8, 15, 9, 55, 0, 0, time.UTC)
	require.NoError(t, runtimeA.Drain())

	now = time.Date(2026, 8, 15, 10, 1, 0, 0, time.UTC)
	require.NoError(t, runtimeB.Snapshot())
	seal, err := runtimeB.SealHour(time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, []string{"runtime-a", "runtime-b"}, seal.RuntimeIDs)
}

func TestMissingRuntimeDrainCreatesDurableDiscontinuity(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)
	root := t.TempDir()
	_, err := Open(root, "runtime-a", now, func() time.Time { return now })
	require.NoError(t, err)

	now = time.Date(2026, 8, 15, 10, 10, 0, 0, time.UTC)
	runtimeB, err := Open(root, "runtime-b", now, func() time.Time { return now })
	require.NoError(t, err)
	health, err := runtimeB.Health()
	require.NoError(t, err)
	require.Equal(t, HealthFailed, health.Status)
	require.Equal(t, "runtime_discontinuity", health.Reason)

	state, err := loadState(root)
	require.NoError(t, err)
	require.Len(t, state.failures, 1)
	require.Equal(t, "runtime_discontinuity", state.failures[0].Stage)
	require.Equal(t, "unresolved", state.failures[0].Status)

	_, err = runtimeB.SealHour(time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, ErrHourUnsealed)
}

func TestMissingRuntimeDrainBlocksOnlyRecordedDiscontinuityInterval(t *testing.T) {
	now := time.Date(2026, 8, 15, 8, 30, 0, 0, time.UTC)
	root := t.TempDir()
	_, err := Open(root, "runtime-a", now, func() time.Time { return now })
	require.NoError(t, err)

	now = time.Date(2026, 8, 15, 10, 10, 0, 0, time.UTC)
	runtimeB, err := Open(root, "runtime-b", now, func() time.Time { return now })
	require.NoError(t, err)

	_, err = runtimeB.SealHour(time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, ErrHourUnsealed, "the recorded discontinuity must block an overlapping hour")

	now = time.Date(2026, 8, 15, 12, 1, 0, 0, time.UTC)
	require.NoError(t, runtimeB.Snapshot())
	seal, err := runtimeB.SealHour(time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC))
	require.NoError(t, err, "the stale runtime must not intersect hours after its last durable snapshot")
	require.Equal(t, []string{"runtime-b"}, seal.RuntimeIDs)
}

func TestHourSealIgnoresLaterHeartbeatButRejectsNewSourceHourWork(t *testing.T) {
	now := time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)
	root := t.TempDir()
	ledger, err := Open(root, "runtime-a", now, func() time.Time { return now })
	require.NoError(t, err)

	now = time.Date(2026, 8, 15, 10, 1, 0, 0, time.UTC)
	require.NoError(t, ledger.Snapshot())
	_, err = ledger.SealHour(time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	now = now.Add(time.Minute)
	require.NoError(t, ledger.Snapshot())
	_, err = ValidateHourSeal(root, time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC), now, DefaultFreshness)
	require.NoError(t, err)

	require.NoError(t, ledger.Begin(CaptureIdentity{
		RequestID:  "late-old-hour-work",
		CapturedAt: time.Date(2026, 8, 15, 9, 50, 0, 0, time.UTC),
	}))
	_, err = ValidateHourSeal(root, time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC), now, DefaultFreshness)
	require.ErrorIs(t, err, ErrHourUnsealed)
}

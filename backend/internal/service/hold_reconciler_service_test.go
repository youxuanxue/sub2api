//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type reconcilerHoldApplier struct {
	UsageBillingRepository
	refunded  int
	sweepErr  error
	olderThan time.Time
	batch     int
	calls     int
}

func (r *reconcilerHoldApplier) ReserveBalanceHold(context.Context, *HoldCommand) (bool, error) {
	return true, nil
}

func (r *reconcilerHoldApplier) ReleaseBalanceHold(context.Context, string) (bool, error) {
	return true, nil
}

func (r *reconcilerHoldApplier) ReleaseExpiredBalanceHolds(_ context.Context, olderThan time.Time, batch int) (int, error) {
	r.calls++
	r.olderThan, r.batch = olderThan, batch
	return r.refunded, r.sweepErr
}

type recordingHeartbeat struct {
	mu    sync.Mutex
	beats []*OpsUpsertJobHeartbeatInput
}

func (h *recordingHeartbeat) UpsertJobHeartbeat(_ context.Context, in *OpsUpsertJobHeartbeatInput) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beats = append(h.beats, in)
	return nil
}

func (h *recordingHeartbeat) last() *OpsUpsertJobHeartbeatInput {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.beats) == 0 {
		return nil
	}
	return h.beats[len(h.beats)-1]
}

// The reconciler is the last line of defence for user balance. Without a
// heartbeat there was no way to tell a healthy no-op sweep from a reconciler
// that was never scheduled — which is exactly the ambiguity that made the
// production hold leak hard to triage.
func TestHoldReconciler_WritesHeartbeatOnSuccessfulSweep(t *testing.T) {
	applier := &reconcilerHoldApplier{refunded: 3}
	heartbeat := &recordingHeartbeat{}
	svc := NewHoldReconcilerService(applier, heartbeat)

	svc.reconcileOnce()

	require.Equal(t, 1, applier.calls)
	beat := heartbeat.last()
	require.NotNil(t, beat, "a sweep must record a heartbeat")
	require.Equal(t, HoldReconcilerJobName, beat.JobName)
	require.NotNil(t, beat.LastRunAt)
	require.NotNil(t, beat.LastSuccessAt)
	require.NotNil(t, beat.LastResult)
	require.Contains(t, *beat.LastResult, "refunded=3")
	require.Nil(t, beat.LastError)
}

func TestHoldReconciler_WritesHeartbeatOnFailedSweep(t *testing.T) {
	applier := &reconcilerHoldApplier{sweepErr: errors.New("database unavailable")}
	heartbeat := &recordingHeartbeat{}
	svc := NewHoldReconcilerService(applier, heartbeat)

	svc.reconcileOnce()

	beat := heartbeat.last()
	require.NotNil(t, beat)
	require.NotNil(t, beat.LastErrorAt)
	require.NotNil(t, beat.LastError)
	require.Contains(t, *beat.LastError, "database unavailable")
	require.Nil(t, beat.LastSuccessAt, "a failed sweep must not report success")
}

// A heartbeat is observability, never a precondition for refunding balance.
func TestHoldReconciler_SweepsWithoutHeartbeatDependency(t *testing.T) {
	applier := &reconcilerHoldApplier{refunded: 1}
	svc := NewHoldReconcilerService(applier, nil)

	svc.reconcileOnce()

	require.Equal(t, 1, applier.calls, "a nil heartbeat must not stop the sweep")
}

func TestHoldReconciler_SweepUsesTTLAndBatch(t *testing.T) {
	applier := &reconcilerHoldApplier{}
	svc := NewHoldReconcilerService(applier, nil)

	before := time.Now()
	svc.reconcileOnce()

	require.Equal(t, 500, applier.batch)
	// TTL must comfortably exceed the longest legitimate request so a running
	// stream is never refunded out from under itself.
	require.GreaterOrEqual(t, svc.ttl, 30*time.Minute)
	require.WithinDuration(t, before.Add(-svc.ttl), applier.olderThan, 5*time.Second)
}

// Start is a no-op when the repository lacks the hold capability, so a
// deployment without holds does not spin a pointless ticker.
func TestHoldReconciler_StartNoOpWithoutHoldCapability(t *testing.T) {
	svc := NewHoldReconcilerService(struct{ UsageBillingRepository }{}, nil)
	require.Nil(t, svc.applier)
	svc.Start()
	svc.Stop()
}

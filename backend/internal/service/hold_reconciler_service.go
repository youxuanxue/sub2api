package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// HoldReconcilerService refunds leaked pre-flight balance holds — the
// crash-recovery backstop for the overdraft fix (see usage_billing_hold_tk.go).
//
// A hold is reserved before forward and released at request end. The normal
// release runs in the request goroutine, so a process crash (or a panic past
// the defer) mid-request can leave a usage_holds row with the balance still
// reduced and no matching bill. This ticker sweeps holds older than the TTL and
// refunds them.
//
// TTL must exceed the longest legitimate request duration, or a still-running
// long stream would have its hold refunded early (losing overdraft protection
// for that one request until it ends). 30m comfortably covers streaming; the
// only cost of a generous TTL is that a genuinely leaked hold is refunded a bit
// later. The refund is conservative either way — a leaked hold over-charges the
// user (their balance is held for unrendered service), never the operator.

// HoldReconcilerJobName is the ops_job_heartbeats row this sweep writes. The
// reconciler is the last line of defence for user balance, so its liveness must
// be observable the same way every other scheduled job is: before this existed
// there was no way to tell a healthy no-op sweep apart from a reconciler that
// was never scheduled at all.
const HoldReconcilerJobName = "hold_reconciler"

// holdReconcilerHeartbeat is the narrow heartbeat dependency (OpsRepository
// satisfies it). nil → heartbeat is skipped and the sweep still runs.
type holdReconcilerHeartbeat interface {
	UpsertJobHeartbeat(ctx context.Context, in *OpsUpsertJobHeartbeatInput) error
}

// HoldReconcilerService owns the periodic leaked-hold sweep described above.
type HoldReconcilerService struct {
	applier   UsageBillingHoldApplier
	heartbeat holdReconcilerHeartbeat
	interval  time.Duration
	ttl       time.Duration
	batch     int

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
}

// NewHoldReconcilerService builds the reconciler. If the repository does not
// implement the hold capability (UsageBillingHoldApplier), Start is a no-op.
func NewHoldReconcilerService(repo UsageBillingRepository, heartbeat holdReconcilerHeartbeat) *HoldReconcilerService {
	applier, _ := repo.(UsageBillingHoldApplier)
	return &HoldReconcilerService{
		applier:   applier,
		heartbeat: heartbeat,
		interval:  60 * time.Second,
		ttl:       30 * time.Minute,
		batch:     500,
		stopCh:    make(chan struct{}),
	}
}

func (s *HoldReconcilerService) Start() {
	if s == nil || s.applier == nil {
		return
	}
	s.startOnce.Do(func() {
		logger.LegacyPrintf("service.hold_reconciler", "[HoldReconciler] started interval=%s ttl=%s batch=%d", s.interval, s.ttl, s.batch)
		go s.runLoop()
	})
}

func (s *HoldReconcilerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		logger.LegacyPrintf("service.hold_reconciler", "[HoldReconciler] stopped")
	})
}

func (s *HoldReconcilerService) runLoop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Sweep once on start to drain anything leaked across a restart.
	s.reconcileOnce()

	for {
		select {
		case <-ticker.C:
			s.reconcileOnce()
		case <-s.stopCh:
			return
		}
	}
}

func (s *HoldReconcilerService) reconcileOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	startedAt := time.Now()
	olderThan := startedAt.Add(-s.ttl)
	refunded, err := s.applier.ReleaseExpiredBalanceHolds(ctx, olderThan, s.batch)
	if err != nil {
		logger.LegacyPrintf("service.hold_reconciler", "[HoldReconciler] sweep failed err=%v", err)
		s.writeHeartbeat(startedAt, refunded, err)
		return
	}
	if refunded > 0 {
		// Non-zero means real crash leaks were refunded — surface for ops.
		logger.LegacyPrintf("service.hold_reconciler", "[HoldReconciler] refunded leaked holds count=%d older_than=%s", refunded, olderThan.Format(time.RFC3339))
	}
	s.writeHeartbeat(startedAt, refunded, nil)
}

// writeHeartbeat records this sweep in ops_job_heartbeats. Best-effort: a
// heartbeat write failure must never stop refunding user balance.
func (s *HoldReconcilerService) writeHeartbeat(startedAt time.Time, refunded int, sweepErr error) {
	if s.heartbeat == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	durationMs := time.Since(startedAt).Milliseconds()
	in := &OpsUpsertJobHeartbeatInput{
		JobName:        HoldReconcilerJobName,
		LastRunAt:      &startedAt,
		LastDurationMs: &durationMs,
	}
	if sweepErr != nil {
		now := time.Now()
		msg := sweepErr.Error()
		in.LastErrorAt, in.LastError = &now, &msg
	} else {
		now := time.Now()
		result := fmt.Sprintf("refunded=%d ttl=%s", refunded, s.ttl)
		in.LastSuccessAt, in.LastResult = &now, &result
	}
	_ = s.heartbeat.UpsertJobHeartbeat(ctx, in)
}

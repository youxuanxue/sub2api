package service

import (
	"context"
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
type HoldReconcilerService struct {
	applier  UsageBillingHoldApplier
	interval time.Duration
	ttl      time.Duration
	batch    int

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
}

// NewHoldReconcilerService builds the reconciler. If the repository does not
// implement the hold capability (UsageBillingHoldApplier), Start is a no-op.
func NewHoldReconcilerService(repo UsageBillingRepository) *HoldReconcilerService {
	applier, _ := repo.(UsageBillingHoldApplier)
	return &HoldReconcilerService{
		applier:  applier,
		interval: 60 * time.Second,
		ttl:      30 * time.Minute,
		batch:    500,
		stopCh:   make(chan struct{}),
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

	olderThan := time.Now().Add(-s.ttl)
	refunded, err := s.applier.ReleaseExpiredBalanceHolds(ctx, olderThan, s.batch)
	if err != nil {
		logger.LegacyPrintf("service.hold_reconciler", "[HoldReconciler] sweep failed err=%v", err)
		return
	}
	if refunded > 0 {
		// Non-zero means real crash leaks were refunded — surface for ops.
		logger.LegacyPrintf("service.hold_reconciler", "[HoldReconciler] refunded leaked holds count=%d older_than=%s", refunded, olderThan.Format(time.RFC3339))
	}
}

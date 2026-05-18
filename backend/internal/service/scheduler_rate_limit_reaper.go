package service

// TK: See upstream Wei-Shaw/sub2api#2538 — when an account hits 429, the
// scheduler outbox immediately rebuilds the bucket and the account is dropped
// from the snapshot because `ListSchedulableByGroupIDAndPlatform` SQL filters
// `RateLimitResetAtIsNil OR RateLimitResetAtLTE(now)`. Nothing fires when the
// cooldown later expires, so the account is invisible to scheduling until the
// next full-rebuild tick (default 5 minutes) — long enough for ops to perceive
// "high-priority account never recovers".
//
// SchedulerRateLimitReaper closes the gap with a short tick that scans
// `accounts.rate_limit_reset_at` falling in (since, now] and enqueues a single
// `account_changed` outbox event per account. The existing outbox worker then
// runs `loadAccountsFromDB` (which no longer excludes the now-expired account)
// and writes a fresh snapshot. The outbox INSERT already has a 1-second dedup
// window, so even sub-second reaper intervals do not flood the table.
//
// This module is deliberately a TK companion: it does not modify upstream
// `scheduler_snapshot_service.go` and degrades to a no-op when
// RateLimitExpiryRepository is nil, so it can be enabled or disabled via
// config without touching the upstream snapshot pipeline.

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// RateLimitExpiryRepository drives the reaper. The repository implementation
// must run a single atomic SQL that enqueues outbox `account_changed` events
// for every account whose rate-limit cooldown expired inside (since, until].
type RateLimitExpiryRepository interface {
	// EnqueueOutboxForJustExpiredAccounts inserts one
	// scheduler_outbox(event_type='account_changed') row per matching account
	// and returns the number of rows inserted. Caller chooses the window;
	// SchedulerRateLimitReaper passes (now - lookback, now] each tick.
	EnqueueOutboxForJustExpiredAccounts(ctx context.Context, since, until time.Time) (int, error)
}

// SchedulerRateLimitReaper is the TK companion goroutine that triggers
// snapshot rebuilds when an account's rate-limit cooldown elapses. See the
// file-level comment for the upstream-issue context.
type SchedulerRateLimitReaper struct {
	repo     RateLimitExpiryRepository
	cfg      *config.Config
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	// lastTick is the upper bound of the previous reaper tick. Kept on the
	// receiver so the next tick uses (lastTick, now] and consecutive ticks
	// never miss accounts whose reset_at falls inside the gap between ticks.
	lastTick time.Time
}

// NewSchedulerRateLimitReaper constructs the reaper. A nil repo or zero-tick
// interval produces a reaper that no-ops on Start, which keeps wire wiring
// safe even when tests inject minimal dependencies.
func NewSchedulerRateLimitReaper(repo RateLimitExpiryRepository, cfg *config.Config) *SchedulerRateLimitReaper {
	return &SchedulerRateLimitReaper{
		repo:   repo,
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

// Start launches the reaper goroutine. Safe to call exactly once; subsequent
// Start calls are no-ops. The goroutine exits when Stop is called.
func (r *SchedulerRateLimitReaper) Start() {
	if r == nil || r.repo == nil {
		return
	}
	interval := r.tickInterval()
	if interval <= 0 {
		return
	}
	r.lastTick = time.Now()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.runOnce()
			case <-r.stopCh:
				return
			}
		}
	}()
}

// Stop signals the goroutine to exit and waits for it. Safe to call exactly
// once; subsequent Stop calls are no-ops.
func (r *SchedulerRateLimitReaper) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
	r.wg.Wait()
}

// runOnce performs a single reaper pass. Exported for tests; production code
// always reaches it via the ticker.
func (r *SchedulerRateLimitReaper) runOnce() {
	if r == nil || r.repo == nil {
		return
	}
	now := time.Now()
	lookback := r.lookback()
	since := r.lastTick
	if since.IsZero() || now.Sub(since) > lookback {
		// First tick after Start, or wall-clock jumped forward (e.g.
		// container resume). Fall back to a fixed lookback window so we
		// still catch every account whose cooldown expired recently
		// without trying to cover an unbounded history.
		since = now.Add(-lookback)
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.queryTimeout())
	defer cancel()

	inserted, err := r.repo.EnqueueOutboxForJustExpiredAccounts(ctx, since, now)
	if err != nil {
		logger.LegacyPrintf("service.scheduler_rate_limit_reaper",
			"[Scheduler] rate-limit reaper enqueue failed: since=%s err=%v",
			since.Format(time.RFC3339), err)
		// Keep lastTick unchanged so the next tick re-attempts this window.
		return
	}
	r.lastTick = now
	if inserted > 0 {
		logger.LegacyPrintf("service.scheduler_rate_limit_reaper",
			"[Scheduler] rate-limit reaper enqueued %d account_changed events for cooldown-expired accounts (since=%s)",
			inserted, since.Format(time.RFC3339))
	}
}

func (r *SchedulerRateLimitReaper) tickInterval() time.Duration {
	if r.cfg == nil {
		return 5 * time.Second
	}
	sec := r.cfg.Gateway.Scheduling.RateLimitReaperIntervalSeconds
	if sec < 0 {
		return 0
	}
	if sec == 0 {
		return 5 * time.Second
	}
	return time.Duration(sec) * time.Second
}

func (r *SchedulerRateLimitReaper) lookback() time.Duration {
	if r.cfg == nil {
		return 30 * time.Second
	}
	sec := r.cfg.Gateway.Scheduling.RateLimitReaperLookbackSeconds
	if sec <= 0 {
		return 30 * time.Second
	}
	return time.Duration(sec) * time.Second
}

func (r *SchedulerRateLimitReaper) queryTimeout() time.Duration {
	// Hard-coded; the SQL is a single INSERT...SELECT that should always
	// finish in well under a second on production-sized account tables.
	return 5 * time.Second
}

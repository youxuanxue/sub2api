//go:build unit

package service

// TK: See upstream Wei-Shaw/sub2api#2538 — these tests pin the reaper's
// invariants for the "high-priority account stuck in cooldown limbo"
// deadlock. The reaper runs as a goroutine in production, but each test
// drives runOnce() directly so we never spin on the wall clock.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// fakeRateLimitExpiryRepo records every (since, until) window the reaper
// inspects and returns a scripted (inserted, err) per call. Stays goroutine-
// safe so a future timer-driven test can race against it without flakes.
type fakeRateLimitExpiryRepo struct {
	mu       sync.Mutex
	calls    []rateLimitExpiryCall
	inserted []int
	errors   []error
	idx      int
}

type rateLimitExpiryCall struct {
	since time.Time
	until time.Time
}

func (f *fakeRateLimitExpiryRepo) EnqueueOutboxForJustExpiredAccounts(ctx context.Context, since, until time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rateLimitExpiryCall{since: since, until: until})
	if f.idx < len(f.errors) && f.errors[f.idx] != nil {
		err := f.errors[f.idx]
		f.idx++
		return 0, err
	}
	val := 0
	if f.idx < len(f.inserted) {
		val = f.inserted[f.idx]
	}
	f.idx++
	return val, nil
}

func (f *fakeRateLimitExpiryRepo) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRateLimitExpiryRepo) lastCall() rateLimitExpiryCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return rateLimitExpiryCall{}
	}
	return f.calls[len(f.calls)-1]
}

// TestRateLimitReaper_FirstTick_UsesLookbackWindow locks the contract that on
// the very first tick after Start (or after lastTick is zero), the reaper
// scans (now - lookback, now] rather than skipping the window. Without this,
// accounts whose cooldown expired between server start and the first reaper
// tick would stay invisible until the slow full_rebuild_interval_seconds tick.
func TestRateLimitReaper_FirstTick_UsesLookbackWindow(t *testing.T) {
	repo := &fakeRateLimitExpiryRepo{inserted: []int{0}}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.RateLimitReaperLookbackSeconds = 30

	reaper := NewSchedulerRateLimitReaper(repo, cfg)
	require.NotNil(t, reaper)

	before := time.Now()
	reaper.runOnce()
	after := time.Now()

	require.Equal(t, 1, repo.callCount(), "reaper should issue exactly one repository call per tick")
	call := repo.lastCall()
	gap := call.until.Sub(call.since)
	require.InDelta(t, 30*time.Second, gap, float64(time.Second),
		"first tick window must equal the configured lookback")
	require.True(t, !call.until.Before(before) && !call.until.After(after),
		"window upper bound must be the reaper's wall-clock now")
}

// TestRateLimitReaper_SubsequentTicks_UseLastTickAsLowerBound is the core
// #2538 invariant: between ticks the reaper must scan exactly the gap so no
// account whose cooldown expired in (lastTick, now] is missed. A weaker
// implementation that always used (now-lookback, now] would still work but
// would either (a) miss accounts when lookback < tick interval or (b)
// repeatedly enqueue the same accounts when lookback > tick interval.
func TestRateLimitReaper_SubsequentTicks_UseLastTickAsLowerBound(t *testing.T) {
	repo := &fakeRateLimitExpiryRepo{inserted: []int{0, 0}}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.RateLimitReaperLookbackSeconds = 30

	reaper := NewSchedulerRateLimitReaper(repo, cfg)
	reaper.runOnce()
	firstUpper := repo.lastCall().until

	// Simulate the next tick a short time later.
	time.Sleep(20 * time.Millisecond)
	reaper.runOnce()

	require.Equal(t, 2, repo.callCount())
	secondCall := repo.lastCall()
	require.True(t, secondCall.since.Equal(firstUpper) || secondCall.since.After(firstUpper.Add(-time.Millisecond)),
		"second tick lower bound must continue from the first tick's upper bound (no gap, no double-scan)")
}

// TestRateLimitReaper_RepoErrorPreservesLastTick prevents the regression
// where a transient DB error advances lastTick and silently drops the missed
// window. The reaper must keep lastTick unchanged so the next tick re-attempts
// the same window.
func TestRateLimitReaper_RepoErrorPreservesLastTick(t *testing.T) {
	repo := &fakeRateLimitExpiryRepo{
		inserted: []int{0, 0, 0},
		errors:   []error{nil, errors.New("transient db error"), nil},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.RateLimitReaperLookbackSeconds = 30

	reaper := NewSchedulerRateLimitReaper(repo, cfg)

	reaper.runOnce()
	firstUpper := repo.lastCall().until

	time.Sleep(20 * time.Millisecond)
	reaper.runOnce() // returns error
	errorCallUpper := repo.lastCall().until

	time.Sleep(20 * time.Millisecond)
	reaper.runOnce()
	thirdCall := repo.lastCall()

	require.Equal(t, 3, repo.callCount())
	// After the error, lastTick must NOT have advanced to errorCallUpper —
	// instead the third call's lower bound must still be firstUpper (the
	// last successful tick's upper bound).
	require.True(t, thirdCall.since.Equal(firstUpper) || thirdCall.since.After(firstUpper.Add(-time.Millisecond)),
		"after a transient error, the next tick must re-attempt from the last successful tick's upper bound, not from the failed tick's upper bound")
	require.True(t, thirdCall.since.Before(errorCallUpper),
		"after error, next tick lower bound must precede the failed tick's upper bound so missed accounts are caught up")
}

// TestRateLimitReaper_NilRepo_NoOp confirms the reaper degrades to a safe
// no-op when wire injects a nil repository (e.g. simple_run mode or test
// fixtures that skip the DB plumbing).
func TestRateLimitReaper_NilRepo_NoOp(t *testing.T) {
	reaper := NewSchedulerRateLimitReaper(nil, &config.Config{})
	require.NotPanics(t, func() {
		reaper.runOnce()
		reaper.Start()
		reaper.Stop()
	})
}

// TestRateLimitReaper_NegativeIntervalDisablesGoroutine verifies the explicit
// disable knob (interval < 0). With this off, no goroutine is spawned and
// Stop() must still be safe.
func TestRateLimitReaper_NegativeIntervalDisablesGoroutine(t *testing.T) {
	repo := &fakeRateLimitExpiryRepo{}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.RateLimitReaperIntervalSeconds = -1

	reaper := NewSchedulerRateLimitReaper(repo, cfg)
	reaper.Start()
	reaper.Stop()

	require.Equal(t, 0, repo.callCount(),
		"a negative interval must disable the reaper entirely (no repository calls, no goroutine)")
}

// TestRateLimitReaper_StartStop_IsIdempotentAndDoesNotLeak runs the full
// lifecycle path (Start → tick → Stop) and confirms the goroutine exits
// cleanly. The test intentionally uses a short interval so the tick fires
// at least once inside the sleep window without burdening CI.
func TestRateLimitReaper_StartStop_IsIdempotentAndDoesNotLeak(t *testing.T) {
	repo := &fakeRateLimitExpiryRepo{inserted: []int{0, 0, 0, 0, 0, 0, 0, 0}}
	cfg := &config.Config{}
	// Use a tiny interval so the ticker fires inside the test budget;
	// production default is 5 s, set in viper defaults.
	cfg.Gateway.Scheduling.RateLimitReaperIntervalSeconds = 1
	cfg.Gateway.Scheduling.RateLimitReaperLookbackSeconds = 30

	reaper := NewSchedulerRateLimitReaper(repo, cfg)
	reaper.Start()
	defer reaper.Stop()

	require.Eventually(t, func() bool {
		return repo.callCount() >= 1
	}, 3*time.Second, 50*time.Millisecond,
		"reaper goroutine must dispatch at least one tick after Start")

	// A second Stop must not block or panic.
	reaper.Stop()
	reaper.Stop()
}

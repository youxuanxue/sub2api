//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

// Regression tests for the opt-out kill-switch read path.
//
// Root cause being pinned here: settingRepo.GetValue returns ErrSettingNotFound
// for a key that was never written, and the accessors used to treat that as a
// storage failure — warning on every read and caching for the short errorTTL.
// On prod (1.8.173) that produced 603 of 869 warn/error rows in 50 minutes for
// two keys that were simply absent, at 12x the intended read rate.
//
// The invariants below are what must not regress:
//   - absent key  → default ON, NO warning, long (ok) TTL
//   - real DB err → default ON, warning, short (error) TTL
//   - resolved enabled value is unchanged for every input

// optOutFlagRepoStub returns a fixed (value, error) for any key.
type optOutFlagRepoStub struct {
	SettingRepository
	val  string
	err  error
	hits int64
}

func (r *optOutFlagRepoStub) GetValue(_ context.Context, _ string) (string, error) {
	atomic.AddInt64(&r.hits, 1)
	return r.val, r.err
}

func (r *optOutFlagRepoStub) calls() int64 { return atomic.LoadInt64(&r.hits) }

// --- the decision table (pure function, no cache/service state) -------------

func TestResolveOptOutFlagValue_AbsentKeyIsNotAFailure(t *testing.T) {
	okTTL := 60 * time.Second
	errorTTL := 5 * time.Second

	enabled, ttl, warn := tkResolveOptOutFlagValue("", ErrSettingNotFound, okTTL, errorTTL)

	require.True(t, enabled, "absent key must fail open to the default-ON value")
	require.False(t, warn, "absent key is the normal steady state, not a fault: must not warn")
	require.Equal(t, okTTL, ttl,
		"absent key must cache for the long TTL; errorTTL here is the 12x read amplifier")
}

func TestResolveOptOutFlagValue_WrappedNotFoundStillCountsAsAbsent(t *testing.T) {
	wrapped := fmt.Errorf("query settings: %w", ErrSettingNotFound)

	enabled, ttl, warn := tkResolveOptOutFlagValue("", wrapped, 60*time.Second, 5*time.Second)

	require.True(t, enabled)
	require.False(t, warn, "errors.Is must see through wrapping")
	require.Equal(t, 60*time.Second, ttl)
}

func TestResolveOptOutFlagValue_RealDBErrorStillWarnsAndBacksOff(t *testing.T) {
	okTTL := 60 * time.Second
	errorTTL := 5 * time.Second

	enabled, ttl, warn := tkResolveOptOutFlagValue("", errors.New("connection refused"), okTTL, errorTTL)

	require.True(t, enabled, "a storage fault must still fail open (gateway behavior preserved)")
	require.True(t, warn, "a real storage fault must stay visible to operators")
	require.Equal(t, errorTTL, ttl, "a transient fault should retry quickly")
}

func TestResolveOptOutFlagValue_EnabledValueUnchangedForAllInputs(t *testing.T) {
	okTTL := 60 * time.Second
	errorTTL := 5 * time.Second

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty defaults true", "", true},
		{"explicit true", "true", true},
		{"explicit false disables", "false", false},
		{"padded false still disables", "  false  ", false},
		{"garbage defaults true (opt-out, not opt-in)", "yes", true},
		{"mixed case is not the literal false", "False", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enabled, ttl, warn := tkResolveOptOutFlagValue(tc.raw, nil, okTTL, errorTTL)
			require.Equal(t, tc.want, enabled)
			require.False(t, warn, "a successful read must never warn")
			require.Equal(t, okTTL, ttl)
		})
	}
}

// --- the cached read path ---------------------------------------------------

func newTestOptOutSpec(t *testing.T, key string) tkOptOutFlagSpec {
	t.Helper()
	return tkOptOutFlagSpec{
		key:       key,
		warnMsg:   "test flag read failed",
		cache:     &atomic.Value{},
		sf:        &singleflight.Group{},
		okTTL:     60 * time.Second,
		errorTTL:  5 * time.Second,
		dbTimeout: 5 * time.Second,
	}
}

// The 12x amplifier, expressed as a cache assertion: an absent key must be
// cached for okTTL, so a second read inside that window must not hit the repo.
func TestReadOptOutFlag_AbsentKeyIsCachedForOkTTL(t *testing.T) {
	repo := &optOutFlagRepoStub{err: ErrSettingNotFound}
	spec := newTestOptOutSpec(t, "gateway.test_absent.enabled")

	require.True(t, tkReadOptOutFlag(context.Background(), repo, spec))
	require.Equal(t, int64(1), repo.calls())

	require.True(t, tkReadOptOutFlag(context.Background(), repo, spec))
	require.Equal(t, int64(1), repo.calls(),
		"absent key must be cached like a successful read, not re-read every errorTTL")
}

func TestReadOptOutFlag_RealErrorUsesShortTTLSoItRetries(t *testing.T) {
	repo := &optOutFlagRepoStub{err: errors.New("connection refused")}
	spec := newTestOptOutSpec(t, "gateway.test_dberr.enabled")
	// errorTTL already elapsed by the time the second call happens.
	spec.errorTTL = -1 * time.Second

	require.True(t, tkReadOptOutFlag(context.Background(), repo, spec))
	require.True(t, tkReadOptOutFlag(context.Background(), repo, spec))

	require.Equal(t, int64(2), repo.calls(),
		"a real storage fault must keep retrying on the short TTL")
}

func TestReadOptOutFlag_DisabledValueIsHonoredAndCached(t *testing.T) {
	repo := &optOutFlagRepoStub{val: "false"}
	spec := newTestOptOutSpec(t, "gateway.test_off.enabled")

	require.False(t, tkReadOptOutFlag(context.Background(), repo, spec))
	require.False(t, tkReadOptOutFlag(context.Background(), repo, spec))
	require.Equal(t, int64(1), repo.calls())
}

func TestReadOptOutFlag_NilRepoFailsOpen(t *testing.T) {
	spec := newTestOptOutSpec(t, "gateway.test_nilrepo.enabled")
	require.True(t, tkReadOptOutFlag(context.Background(), nil, spec))
}

// A canceled caller context must not poison the read: the DB call runs on a
// detached context (context.WithoutCancel) precisely so one canceled request
// cannot make the flag look broken for everyone.
func TestReadOptOutFlag_CanceledCallerContextStillReads(t *testing.T) {
	repo := &optOutFlagRepoStub{val: "false"}
	spec := newTestOptOutSpec(t, "gateway.test_canceled.enabled")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.False(t, tkReadOptOutFlag(ctx, repo, spec),
		"the setting value must still be resolved when the caller's context is done")
	require.Equal(t, int64(1), repo.calls())
}

// Concurrent cold-start readers must collapse into a single DB read.
func TestReadOptOutFlag_ConcurrentReadsCollapse(t *testing.T) {
	repo := &optOutFlagRepoStub{err: ErrSettingNotFound}
	spec := newTestOptOutSpec(t, "gateway.test_singleflight.enabled")

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.True(t, tkReadOptOutFlag(context.Background(), repo, spec))
		}()
	}
	wg.Wait()

	require.LessOrEqual(t, repo.calls(), int64(2),
		"singleflight must collapse concurrent cold reads")
}

// --- the four real accessors ------------------------------------------------

// Every flag in this family must treat an absent key as the default without
// warning. This is the guard against a future flag being added with the old
// `if err != nil` shape (the #743 / #1246 mistake).
func TestAllOptOutAccessors_AbsentKeyDefaultsOnWithoutChurn(t *testing.T) {
	accessors := []struct {
		name  string
		reset func()
		call  func(*SettingService, context.Context) bool
	}{
		{
			name:  "anthropic saturated-stub deprioritize",
			reset: func() { satDeprioritizeCache.Store((*tkOptOutFlagCacheEntry)(nil)) },
			call: func(s *SettingService, ctx context.Context) bool {
				return s.IsAnthropicSaturatedStubDeprioritizeEnabled(ctx)
			},
		},
		{
			name:  "openai saturated-stub deprioritize",
			reset: func() { openaiSatDeprioritizeCache.Store((*tkOptOutFlagCacheEntry)(nil)) },
			call: func(s *SettingService, ctx context.Context) bool {
				return s.IsOpenAISaturatedStubDeprioritizeEnabled(ctx)
			},
		},
		{
			name:  "sticky routing",
			reset: func() { stickyRoutingCache.Store((*tkOptOutFlagCacheEntry)(nil)) },
			call: func(s *SettingService, ctx context.Context) bool {
				return s.IsStickyRoutingEnabled(ctx)
			},
		},
		{
			name:  "sticky slot-full escape",
			reset: func() { stickySlotFullEscapeCache.Store((*tkOptOutFlagCacheEntry)(nil)) },
			call: func(s *SettingService, ctx context.Context) bool {
				return s.IsStickySlotFullEscapeEnabled(ctx)
			},
		},
	}

	for _, a := range accessors {
		t.Run(a.name, func(t *testing.T) {
			a.reset()
			t.Cleanup(a.reset)

			repo := &optOutFlagRepoStub{err: ErrSettingNotFound}
			svc := &SettingService{settingRepo: repo}
			ctx := context.Background()

			require.True(t, a.call(svc, ctx), "absent key must default ON")
			require.Equal(t, int64(1), repo.calls())

			// Second read inside okTTL must be served from cache. If this flag
			// ever regresses to the errorTTL path, this assertion fails.
			require.True(t, a.call(svc, ctx))
			require.Equal(t, int64(1), repo.calls(),
				"absent key must not re-read on the short errorTTL")
		})
	}
}

func TestAllOptOutAccessors_NilServiceFailsOpen(t *testing.T) {
	ctx := context.Background()
	var nilSvc *SettingService

	require.True(t, nilSvc.IsAnthropicSaturatedStubDeprioritizeEnabled(ctx))
	require.True(t, nilSvc.IsOpenAISaturatedStubDeprioritizeEnabled(ctx))
	require.True(t, nilSvc.IsStickyRoutingEnabled(ctx))
	require.True(t, nilSvc.IsStickySlotFullEscapeEnabled(ctx))
}

// Each flag owns an independent cache slot: disabling one must not leak into
// another.
func TestOptOutAccessors_CachesAreIndependent(t *testing.T) {
	reset := func() {
		satDeprioritizeCache.Store((*tkOptOutFlagCacheEntry)(nil))
		openaiSatDeprioritizeCache.Store((*tkOptOutFlagCacheEntry)(nil))
	}
	reset()
	t.Cleanup(reset)

	ctx := context.Background()

	offSvc := &SettingService{settingRepo: &optOutFlagRepoStub{val: "false"}}
	require.False(t, offSvc.IsAnthropicSaturatedStubDeprioritizeEnabled(ctx))

	absentSvc := &SettingService{settingRepo: &optOutFlagRepoStub{err: ErrSettingNotFound}}
	require.True(t, absentSvc.IsOpenAISaturatedStubDeprioritizeEnabled(ctx),
		"openai flag must not inherit the anthropic flag's cached value")
}

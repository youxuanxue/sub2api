//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCheckOpenAIWindowSchedulability_Boundaries(t *testing.T) {
	const (
		threshold = windowUtilStickyThresholdDefault
		reserve   = windowUtilStickyReserveDefault
	)
	cases := []struct {
		name string
		util float64
		want WindowUtilSchedulability
	}{
		{"well below", 0.50, WindowUtilSchedulable},
		{"just below threshold", 0.979, WindowUtilSchedulable},
		{"at threshold => sticky-only", 0.98, WindowUtilStickyOnly},
		{"inside reserve band", 0.99, WindowUtilStickyOnly},
		{"at hard edge => not schedulable", 1.0, WindowUtilNotSchedulable},
		{"above hard edge", 1.001, WindowUtilNotSchedulable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, CheckOpenAIWindowSchedulability(tc.util, threshold, reserve))
		})
	}
}

func TestCheckOpenAIWindowSchedulability_DisabledThreshold(t *testing.T) {
	// A non-positive or >=1 threshold disables the restriction entirely.
	require.Equal(t, WindowUtilSchedulable, CheckOpenAIWindowSchedulability(0.99, 0, 0.12))
	require.Equal(t, WindowUtilSchedulable, CheckOpenAIWindowSchedulability(0.99, 1.0, 0.12))
}

func TestOpenAIAccountWindowUtilization(t *testing.T) {
	now := time.Now()

	t.Run("no extra => no signal", func(t *testing.T) {
		_, ok := openAIAccountWindowUtilization(&Account{}, now)
		require.False(t, ok)
	})
	t.Run("takes the worse of 5h/7d", func(t *testing.T) {
		acc := &Account{Extra: map[string]any{"codex_5h_used_percent": 60.0, "codex_7d_used_percent": 90.0}}
		util, ok := openAIAccountWindowUtilization(acc, now)
		require.True(t, ok)
		require.InDelta(t, 0.90, util, 1e-9)
	})
	t.Run("stale snapshot => no signal", func(t *testing.T) {
		acc := &Account{Extra: map[string]any{
			"codex_5h_used_percent": 99.0,
			"codex_usage_updated_at": now.Add(-3 * time.Hour).Format(time.RFC3339),
		}}
		_, ok := openAIAccountWindowUtilization(acc, now)
		require.False(t, ok, "snapshot older than 2h must not produce a signal (fail-open)")
	})
	t.Run("already-reset window => no signal", func(t *testing.T) {
		acc := &Account{Extra: map[string]any{
			"codex_5h_used_percent": 99.0,
			"codex_5h_reset_at":     now.Add(-1 * time.Minute).Format(time.RFC3339),
		}}
		_, ok := openAIAccountWindowUtilization(acc, now)
		require.False(t, ok, "a window whose reset time has passed must not produce a signal")
	})
}

func TestIsAccountSchedulableForOpenAIWindow(t *testing.T) {
	svc := &OpenAIGatewayService{}
	ctx := context.Background()
	now := time.Now()
	hot := func(p float64) *Account {
		return &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
			"codex_5h_used_percent":  p,
			"codex_usage_updated_at": now.Format(time.RFC3339),
		}}
	}

	t.Run("nil account => schedulable", func(t *testing.T) {
		require.True(t, svc.isAccountSchedulableForOpenAIWindow(ctx, nil, false))
	})
	t.Run("no signal => schedulable (newapi/compat path)", func(t *testing.T) {
		require.True(t, svc.isAccountSchedulableForOpenAIWindow(ctx, &Account{Platform: PlatformNewAPI}, false))
	})
	t.Run("cool account => schedulable both ways", func(t *testing.T) {
		require.True(t, svc.isAccountSchedulableForOpenAIWindow(ctx, hot(50), false))
		require.True(t, svc.isAccountSchedulableForOpenAIWindow(ctx, hot(50), true))
	})
	t.Run("sticky-only band: kept for sticky, dropped for load-balance", func(t *testing.T) {
		require.False(t, svc.isAccountSchedulableForOpenAIWindow(ctx, hot(98), false))
		require.True(t, svc.isAccountSchedulableForOpenAIWindow(ctx, hot(98), true))
	})
	t.Run("not-schedulable band: dropped even for sticky", func(t *testing.T) {
		require.False(t, svc.isAccountSchedulableForOpenAIWindow(ctx, hot(100), false))
		require.False(t, svc.isAccountSchedulableForOpenAIWindow(ctx, hot(100), true))
	})
	t.Run("per-account disable => schedulable", func(t *testing.T) {
		acc := hot(99.5)
		acc.Extra["openai_window_guard_disabled"] = true
		require.True(t, svc.isAccountSchedulableForOpenAIWindow(ctx, acc, false))
	})
	t.Run("global kill-switch => schedulable", func(t *testing.T) {
		disabledCtx := withOpenAIQuotaAutoPauseSettings(ctx, OpsOpenAIAccountQuotaAutoPauseSettings{WindowStickyGuardDisabled: true})
		require.True(t, svc.isAccountSchedulableForOpenAIWindow(disabledCtx, hot(99.5), false))
	})
	t.Run("operator threshold override widens the schedulable range", func(t *testing.T) {
		// With threshold 0.98 + reserve 0.02, a 96%-used account is schedulable.
		overrideCtx := withOpenAIQuotaAutoPauseSettings(ctx, OpsOpenAIAccountQuotaAutoPauseSettings{
			WindowStickyThreshold: 0.98, WindowStickyReserve: 0.02,
		})
		require.True(t, svc.isAccountSchedulableForOpenAIWindow(overrideCtx, hot(96), false))
	})
}

func TestResolveOpenAIWindowStickyThresholds_Precedence(t *testing.T) {
	ctx := context.Background()

	t.Run("built-in defaults when unconfigured", func(t *testing.T) {
		th, res, enabled := resolveOpenAIWindowStickyThresholds(ctx, &Account{})
		require.True(t, enabled)
		require.InDelta(t, windowUtilStickyThresholdDefault, th, 1e-9)
		require.InDelta(t, windowUtilStickyReserveDefault, res, 1e-9)
	})
	t.Run("per-account Extra overrides global", func(t *testing.T) {
		globalCtx := withOpenAIQuotaAutoPauseSettings(ctx, OpsOpenAIAccountQuotaAutoPauseSettings{
			WindowStickyThreshold: 0.90, WindowStickyReserve: 0.05,
		})
		acc := &Account{Extra: map[string]any{"openai_window_sticky_threshold": 0.70, "openai_window_sticky_reserve": 0.10}}
		th, res, enabled := resolveOpenAIWindowStickyThresholds(globalCtx, acc)
		require.True(t, enabled)
		require.InDelta(t, 0.70, th, 1e-9)
		require.InDelta(t, 0.10, res, 1e-9)
	})
	t.Run("global kill-switch disables", func(t *testing.T) {
		disabledCtx := withOpenAIQuotaAutoPauseSettings(ctx, OpsOpenAIAccountQuotaAutoPauseSettings{WindowStickyGuardDisabled: true})
		_, _, enabled := resolveOpenAIWindowStickyThresholds(disabledCtx, &Account{})
		require.False(t, enabled)
	})
}

func TestLeastUtilizedOpenAIAccount(t *testing.T) {
	now := time.Now()
	a := &Account{ID: 1, Extra: map[string]any{"codex_5h_used_percent": 99.0, "codex_usage_updated_at": now.Format(time.RFC3339)}}
	b := &Account{ID: 2, Extra: map[string]any{"codex_5h_used_percent": 98.0, "codex_usage_updated_at": now.Format(time.RFC3339)}}
	c := &Account{ID: 3, Extra: map[string]any{"codex_5h_used_percent": 99.5, "codex_usage_updated_at": now.Format(time.RFC3339)}}
	best := leastUtilizedOpenAIAccount([]*Account{a, b, c}, now)
	require.NotNil(t, best)
	require.Equal(t, int64(2), best.ID, "the account with the most headroom (lowest used%) wins")

	require.Nil(t, leastUtilizedOpenAIAccount(nil, now))
}

// Integration through the priority+LRU selection path (SelectAccountForModel
// WithExclusions -> selectBestAccount): a near-limit account is steered away in
// favour of a cool one, but is still served when it is the only headroom left.
func TestSelectBestAccount_WindowGuard_RoutesAwayFromHotAccount(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Format(time.RFC3339)
	hot := Account{ID: 36001, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
		Extra: map[string]any{"codex_5h_used_percent": 99.0, "codex_usage_updated_at": now}}
	cool := Account{ID: 36002, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
		Extra: map[string]any{"codex_5h_used_percent": 10.0, "codex_usage_updated_at": now}}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{hot, cool}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(36002), account.ID, "the hot (99%) account must be skipped in favour of the cool one despite higher priority")
}

func TestSelectBestAccount_WindowGuard_NeverEmptiesPool(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Format(time.RFC3339)
	// Both accounts are over the hard edge; the guard must not produce an
	// empty-pool error — it re-admits the one with the most headroom.
	hotter := Account{ID: 36101, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
		Extra: map[string]any{"codex_5h_used_percent": 99.5, "codex_usage_updated_at": now}}
	lessHot := Account{ID: 36102, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
		Extra: map[string]any{"codex_5h_used_percent": 98.0, "codex_usage_updated_at": now}}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{hotter, lessHot}}, cfg: &config.Config{}}

	account, err := svc.SelectAccountForModelWithExclusions(ctx, nil, "", "gpt-5.1", nil)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, int64(36102), account.ID, "never-empty-pool fallback must serve the coolest account, not error")
}

// Integration through the LEGACY load-aware path (selectAccountWithLoadAwareness),
// which is the DEFAULT prod path (advanced scheduler off) and the one that served
// the reported gpt-5.5 incident.
func TestSelectAccountWithScheduler_LegacyWindowGuard_RoutesAwayFromHotAccount(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	ctx := context.Background()
	groupID := int64(10301)
	now := time.Now().Format(time.RFC3339)
	accounts := []Account{
		{ID: 38001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
			Extra: map[string]any{"codex_5h_used_percent": 99.0, "codex_usage_updated_at": now}},
		{ID: 38002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
			Extra: map[string]any{"codex_5h_used_percent": 10.0, "codex_usage_updated_at": now}},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	require.False(t, svc.isOpenAIAdvancedSchedulerEnabled(ctx))

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(38002), selection.Account.ID, "legacy path: hot 99% account skipped despite better priority")
}

func TestSelectAccountWithScheduler_LegacyWindowGuard_NeverEmptiesPool(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	ctx := context.Background()
	groupID := int64(10302)
	now := time.Now().Format(time.RFC3339)
	accounts := []Account{
		{ID: 38101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
			Extra: map[string]any{"codex_5h_used_percent": 99.5, "codex_usage_updated_at": now}},
		{ID: 38102, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
			Extra: map[string]any{"codex_5h_used_percent": 98.0, "codex_usage_updated_at": now}},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(38102), selection.Account.ID, "legacy never-empty-pool: coolest of the all-hot pool is served, not an error")
}

// Integration through the ADVANCED scheduler (selectByLoadBalance + weighted TopK).
func TestSelectAccountWithScheduler_AdvancedWindowGuard_RoutesAwayFromHotAccount(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	ctx := context.Background()
	groupID := int64(10303)
	now := time.Now().Format(time.RFC3339)
	accounts := []Account{
		{ID: 39001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0,
			Extra: map[string]any{"codex_5h_used_percent": 99.0, "codex_usage_updated_at": now}},
		{ID: 39002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5,
			Extra: map[string]any{"codex_5h_used_percent": 10.0, "codex_usage_updated_at": now}},
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
	require.True(t, svc.isOpenAIAdvancedSchedulerEnabled(ctx))

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(39002), selection.Account.ID, "advanced path: hot 99% account excluded from the weighted TopK pool")
}

// Retirement gate (#902): the upstream codex auto-pause decision is a permanent
// no-op now that the window-sched tri-state guard is the single window mechanism.
// Even an account well over a configured auto_pause threshold must NOT be paused.
func TestShouldAutoPauseOpenAIAccountByQuota_Retired(t *testing.T) {
	require.True(t, tkOpenAIAutoPauseRetired(), "retirement gate must be on")

	acc := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"codex_5h_used_percent":   99.0,
			"codex_usage_updated_at":  time.Now().Format(time.RFC3339),
			"auto_pause_5h_threshold": 0.90, // would have paused pre-retirement
		},
	}
	// Seed a non-zero global default too, to prove neither account-level nor global
	// thresholds can revive auto-pause.
	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DefaultThreshold5h: 0.90})
	paused, _ := shouldAutoPauseOpenAIAccountByQuota(ctx, acc)
	require.False(t, paused, "auto-pause is retired; it must never fire regardless of thresholds")
}

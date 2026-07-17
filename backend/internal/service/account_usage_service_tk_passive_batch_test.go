//go:build unit

package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

// passiveBatchAccountRepo 实现 GetByID + GetByIDs（按 accounts slice 提供），
// 其余 AccountRepository 方法走 embedded nil 接口（被调用即 panic，证明未触达）。
type passiveBatchAccountRepo struct {
	AccountRepository
	accounts   []Account
	byIDsCalls atomic.Int64
	byIDCalls  atomic.Int64
}

func (r *passiveBatchAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.byIDCalls.Add(1)
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			acc := r.accounts[i]
			return &acc, nil
		}
	}
	return nil, errAccountNotFoundPassiveBatch
}

func (r *passiveBatchAccountRepo) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	r.byIDsCalls.Add(1)
	want := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make([]*Account, 0, len(ids))
	for i := range r.accounts {
		if _, ok := want[r.accounts[i].ID]; ok {
			acc := r.accounts[i]
			out = append(out, &acc)
		}
	}
	return out, nil
}

var errAccountNotFoundPassiveBatch = &passiveBatchErr{"account not found"}

type passiveBatchErr struct{ msg string }

func (e *passiveBatchErr) Error() string { return e.msg }

// passiveBatchUsageLogRepo 统计窗口统计的批量/单查次数。
type passiveBatchUsageLogRepo struct {
	UsageLogRepository
	cost        map[int64]float64
	batchCalls  atomic.Int64
	singleCalls atomic.Int64
}

func (r *passiveBatchUsageLogRepo) GetAccountWindowStatsBatch(_ context.Context, accountIDs []int64, _ time.Time) (map[int64]*usagestats.AccountStats, error) {
	r.batchCalls.Add(1)
	out := make(map[int64]*usagestats.AccountStats, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = &usagestats.AccountStats{Cost: r.cost[id], StandardCost: r.cost[id], Requests: 1}
	}
	return out, nil
}

func (r *passiveBatchUsageLogRepo) GetAccountWindowStats(_ context.Context, accountID int64, _ time.Time) (*usagestats.AccountStats, error) {
	r.singleCalls.Add(1)
	return &usagestats.AccountStats{Cost: r.cost[accountID], StandardCost: r.cost[accountID], Requests: 1}, nil
}

func passiveAnthropicAccount(id int64, windowStart time.Time) Account {
	end := windowStart.Add(5 * time.Hour)
	return Account{
		ID:                 id,
		Platform:           PlatformAnthropic,
		Type:               AccountTypeOAuth,
		Status:             StatusActive,
		SessionWindowStart: &windowStart,
		SessionWindowEnd:   &end,
		Extra: map[string]any{
			"session_window_utilization":   0.30,
			"passive_usage_sampled_at":     windowStart.Add(time.Minute).Format(time.RFC3339Nano),
			"passive_usage_7d_utilization": 0.10,
		},
	}
}

// TestGetPassiveUsageBatch_EqualsSinglePerAccount 证明批量被动用量与逐个
// GET source=passive 单查结果逐字节一致，且窗口统计查询被批量化（每个不同窗口
// 起点一条批量查询，零单查）。
func TestGetPassiveUsageBatch_EqualsSinglePerAccount(t *testing.T) {
	now := time.Now()
	winA := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location()).Add(-2 * time.Hour)
	winB := winA.Add(-1 * time.Hour)

	accounts := []Account{
		passiveAnthropicAccount(1, winA),
		passiveAnthropicAccount(2, winA),
		passiveAnthropicAccount(3, winB),
		// apikey 账号：被动用量不支持，必须被静默省略。
		{ID: 4, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive},
	}
	cost := map[int64]float64{1: 5, 2: 0, 3: 9}

	// 单查黄金参考（每账号一个独立 service，缓存隔离，保证真打仓储）。
	want := make(map[int64]*UsageInfo)
	for _, id := range []int64{1, 2, 3} {
		repo := &passiveBatchAccountRepo{accounts: accounts}
		logRepo := &passiveBatchUsageLogRepo{cost: cost}
		svc := &AccountUsageService{accountRepo: repo, usageLogRepo: logRepo, cache: NewUsageCache()}
		u, err := svc.GetPassiveUsage(context.Background(), id)
		require.NoError(t, err)
		want[id] = u
	}

	// 批量路径。
	repo := &passiveBatchAccountRepo{accounts: accounts}
	logRepo := &passiveBatchUsageLogRepo{cost: cost}
	svc := &AccountUsageService{accountRepo: repo, usageLogRepo: logRepo, cache: NewUsageCache()}
	got := svc.GetPassiveUsageBatch(context.Background(), []int64{1, 2, 3, 4, 1 /*dup*/})

	require.Len(t, got, 3, "apikey account must be omitted; duplicate ID coalesced")
	require.Equal(t, want, got, "batched passive usage must equal per-account single result byte-for-byte")

	// 账号读取批量化：恰好一次 GetByIDs。
	require.Equal(t, int64(1), repo.byIDsCalls.Load(), "accounts must be fetched in one GetByIDs call")

	// 窗口统计批量化：两个不同窗口起点 → 两条批量查询；prefetch 预热缓存后
	// addWindowStats 命中缓存，零单查。
	require.Equal(t, int64(2), logRepo.batchCalls.Load(), "one window-stats batch query per distinct window start")
	require.Zero(t, logRepo.singleCalls.Load(), "prefetched cache must spare per-account window-stats single queries")
}

func TestGetPassiveUsageBatch_IncludesGrokLocalWindows(t *testing.T) {
	accounts := []Account{
		{ID: 9, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive},
	}
	repo := &passiveBatchAccountRepo{accounts: accounts}
	logRepo := &passiveBatchUsageLogRepo{cost: map[int64]float64{9: 1.25}}
	svc := &AccountUsageService{accountRepo: repo, usageLogRepo: logRepo, cache: NewUsageCache()}

	got := svc.GetPassiveUsageBatch(context.Background(), []int64{9})

	require.Len(t, got, 1)
	require.NotNil(t, got[9].FiveHour)
	require.NotNil(t, got[9].SevenDay)
	require.Equal(t, 1.25, got[9].FiveHour.WindowStats.Cost)
	require.Equal(t, int64(2), logRepo.batchCalls.Load(), "grok local 5h/7d windows must be batched in overview")
	require.Zero(t, logRepo.singleCalls.Load(), "grok local overview path must not issue per-account window queries")
}

func TestGetPassiveUsageBatch_IncludesLocalWindowAdapters(t *testing.T) {
	accounts := []Account{
		{ID: 10, Platform: PlatformNewAPI, Type: AccountTypeServiceAccount, ChannelType: 41, Status: StatusActive},
		{ID: 11, Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Status: StatusActive},
		{ID: 12, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive},
	}
	logRepo := &passiveBatchUsageLogRepo{cost: map[int64]float64{10: 2.5, 11: 3.5}}
	svc := &AccountUsageService{
		accountRepo:  &passiveBatchAccountRepo{accounts: accounts},
		usageLogRepo: logRepo,
		cache:        NewUsageCache(),
	}

	got := svc.GetPassiveUsageBatch(context.Background(), []int64{10, 11, 12})

	require.Len(t, got, 2, "newapi and antigravity apikey stubs use local windows; antigravity oauth has no passive adapter")
	require.Equal(t, 2.5, got[10].FiveHour.WindowStats.Cost)
	require.Equal(t, 3.5, got[11].SevenDay.WindowStats.Cost)
	require.NotContains(t, got, int64(12))
	require.Equal(t, int64(2), logRepo.batchCalls.Load(), "local-window accounts share 5h and 7d batch queries")
	require.Zero(t, logRepo.singleCalls.Load(), "local-window overview path must not issue per-account window queries")
}

func TestAccountUsageService_GetUsage_GrokUsesLocalWindowStats(t *testing.T) {
	acct := Account{
		ID:       77,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			grokQuotaSnapshotExtraKey: xai.QuotaSnapshot{
				Requests:         &xai.QuotaWindow{Limit: int64PtrForPassiveBatchTest(900), Remaining: int64PtrForPassiveBatchTest(800)},
				Tokens:           &xai.QuotaWindow{Limit: int64PtrForPassiveBatchTest(15_000_000), Remaining: int64PtrForPassiveBatchTest(14_000_000)},
				SubscriptionTier: "supergrok-heavy",
				StatusCode:       200,
				HeadersObserved:  true,
				UpdatedAt:        "2026-07-02T05:06:30Z",
			},
		},
	}
	logRepo := &passiveBatchUsageLogRepo{cost: map[int64]float64{77: 12.5}}
	svc := &AccountUsageService{
		accountRepo:  &passiveBatchAccountRepo{accounts: []Account{acct}},
		usageLogRepo: logRepo,
	}

	usage, err := svc.GetUsage(context.Background(), 77)

	require.NoError(t, err)
	require.Equal(t, "passive", usage.Source)
	require.NotNil(t, usage.FiveHour)
	require.NotNil(t, usage.FiveHour.WindowStats)
	require.NotNil(t, usage.SevenDay)
	require.NotNil(t, usage.SevenDay.WindowStats)
	require.Equal(t, 12.5, usage.FiveHour.WindowStats.Cost)
	require.NotNil(t, usage.UpstreamQuota)
	require.Equal(t, "grok", usage.UpstreamQuota.Provider)
	require.Equal(t, "observed", usage.UpstreamQuota.State)
	require.Equal(t, "supergrok-heavy", usage.UpstreamQuota.SubscriptionTier)
	require.Len(t, usage.UpstreamQuota.Dimensions, 2)
	require.Equal(t, "grok_requests", usage.UpstreamQuota.Dimensions[0].Key)
	require.Equal(t, float64(800), *usage.UpstreamQuota.Dimensions[0].Remaining)
	require.Equal(t, int64(2), logRepo.singleCalls.Load())
}

func TestAccountUsageService_NewAPIVertexUsesLocalWindowStats(t *testing.T) {
	acct := Account{ID: 78, Platform: PlatformNewAPI, Type: AccountTypeServiceAccount, ChannelType: 41}
	logRepo := &passiveBatchUsageLogRepo{cost: map[int64]float64{78: 4.25}}
	svc := &AccountUsageService{
		accountRepo:  &passiveBatchAccountRepo{accounts: []Account{acct}},
		usageLogRepo: logRepo,
	}

	usage, err := svc.GetUsage(context.Background(), 78)
	require.NoError(t, err)
	require.Equal(t, "passive", usage.Source)
	require.NotNil(t, usage.FiveHour)
	require.NotNil(t, usage.SevenDay)
	require.Equal(t, 4.25, usage.FiveHour.WindowStats.Cost)
	require.NotNil(t, usage.UpstreamQuota)
	require.Equal(t, "newapi", usage.UpstreamQuota.Provider)
	require.Equal(t, "unsupported", usage.UpstreamQuota.State)
	require.Equal(t, "unsupported", usage.UpstreamQuota.ErrorCode)
	require.Equal(t, int64(2), logRepo.singleCalls.Load())

	passive, err := svc.GetPassiveUsage(context.Background(), 78)
	require.NoError(t, err)
	require.NotNil(t, passive.FiveHour)
	require.Equal(t, 4.25, passive.SevenDay.WindowStats.Cost)
	require.Equal(t, "unsupported", passive.UpstreamQuota.State)
}

func TestAccountUsageService_KiroPassiveBuildsUpstreamQuotaCredits(t *testing.T) {
	acct := Account{
		ID:       79,
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"kiro_usage_current":      float64(300),
			"kiro_usage_limit":        float64(1000),
			"kiro_usage_percent":      float64(30),
			"kiro_next_reset":         "2026-07-10",
			"kiro_subscription_title": "Kiro Pro",
			"kiro_usage_sampled_at":   "2026-07-02T00:00:00Z",
		},
	}
	svc := &AccountUsageService{
		accountRepo: &passiveBatchAccountRepo{accounts: []Account{acct}},
		cache:       NewUsageCache(),
	}

	usage, err := svc.GetPassiveUsage(context.Background(), 79)

	require.NoError(t, err)
	require.NotNil(t, usage.KiroUsage)
	require.NotNil(t, usage.UpstreamQuota)
	require.Equal(t, "kiro", usage.UpstreamQuota.Provider)
	require.Equal(t, "observed", usage.UpstreamQuota.State)
	require.Len(t, usage.UpstreamQuota.Credits, 1)
	require.Equal(t, "kiro_credits", usage.UpstreamQuota.Credits[0].Key)
	require.Equal(t, float64(700), *usage.UpstreamQuota.Credits[0].Remaining)
}

func int64PtrForPassiveBatchTest(v int64) *int64 {
	return &v
}

func TestGetPassiveUsageBatch_EmptyAndNilSafe(t *testing.T) {
	svc := &AccountUsageService{
		accountRepo:  &passiveBatchAccountRepo{},
		usageLogRepo: &passiveBatchUsageLogRepo{cost: map[int64]float64{}},
		cache:        NewUsageCache(),
	}
	require.Empty(t, svc.GetPassiveUsageBatch(context.Background(), nil))
	require.Empty(t, svc.GetPassiveUsageBatch(context.Background(), []int64{}))
	require.Empty(t, svc.GetPassiveUsageBatch(context.Background(), []int64{0, -1}))

	var nilSvc *AccountUsageService
	require.Empty(t, nilSvc.GetPassiveUsageBatch(context.Background(), []int64{1}))
}

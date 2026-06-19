//go:build unit

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// windowCostBatchRepoStub 同时记录批量与单查调用次数，并按 (accountID) 返回固定 StandardCost。
// 批量查询按 accountID 命中（与 startTime 无关，模拟真实仓储的行 GROUP BY account_id）。
type windowCostBatchRepoStub struct {
	UsageLogRepository

	standardCost map[int64]float64 // accountID -> StandardCost
	batchErr     error             // 非 nil 时批量查询失败，触发回退
	batchCalls   atomic.Int64
	singleCalls  atomic.Int64
}

func (s *windowCostBatchRepoStub) GetAccountWindowStatsBatch(_ context.Context, accountIDs []int64, _ time.Time) (map[int64]*usagestats.AccountStats, error) {
	s.batchCalls.Add(1)
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	out := make(map[int64]*usagestats.AccountStats, len(accountIDs))
	for _, id := range accountIDs {
		if cost, ok := s.standardCost[id]; ok {
			out[id] = &usagestats.AccountStats{StandardCost: cost}
		}
	}
	return out, nil
}

func (s *windowCostBatchRepoStub) GetAccountWindowStats(_ context.Context, accountID int64, _ time.Time) (*usagestats.AccountStats, error) {
	s.singleCalls.Add(1)
	if cost, ok := s.standardCost[accountID]; ok {
		return &usagestats.AccountStats{StandardCost: cost}, nil
	}
	return &usagestats.AccountStats{}, nil
}

// oauthWindowAccount 构造一个会被纳入窗口费用计算的 Anthropic OAuth 账号，
// 其 GetCurrentWindowStartTime() 由显式的 SessionWindowStart/End 决定。
func oauthWindowAccount(id int64, windowStart time.Time, costLimit float64) Account {
	end := windowStart.Add(5 * time.Hour)
	return Account{
		ID:                 id,
		Platform:           PlatformAnthropic,
		Type:               AccountTypeOAuth,
		SessionWindowStart: &windowStart,
		SessionWindowEnd:   &end,
		Extra:              map[string]any{"window_cost_limit": costLimit},
	}
}

// legacyWindowCostsPerAccount 复刻被替换前的 handler 逐账号实现，作为等价性的黄金参考。
func legacyWindowCostsPerAccount(t *testing.T, repo UsageLogRepository, accounts []Account) map[int64]float64 {
	t.Helper()
	out := make(map[int64]float64)
	for i := range accounts {
		acc := &accounts[i]
		if !acc.IsAnthropicOAuthOrSetupToken() || acc.GetWindowCostLimit() <= 0 {
			continue
		}
		startTime := acc.GetCurrentWindowStartTime()
		stats, err := repo.GetAccountWindowStats(context.Background(), acc.ID, startTime)
		if err == nil && stats != nil {
			out[acc.ID] = stats.StandardCost
		}
	}
	return out
}

func TestGetAccountWindowCostsBatch_EqualsLegacyMultiWindow(t *testing.T) {
	now := time.Now()
	// 两个不同的窗口起点（整点对齐，避免被 GetCurrentWindowStartTime 的过期回退改写）。
	windowA := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location()).Add(-2 * time.Hour)
	windowB := windowA.Add(-1 * time.Hour) // 不同窗口起点

	costs := map[int64]float64{
		1: 12.5,
		2: 0.0, // 命中但费用为 0
		3: 7.25,
		4: 99.0, // 窗口 B
	}

	accounts := []Account{
		oauthWindowAccount(1, windowA, 100), // 窗口 A
		oauthWindowAccount(2, windowA, 100), // 窗口 A，费用 0
		oauthWindowAccount(3, windowA, 100), // 窗口 A
		oauthWindowAccount(4, windowB, 100), // 窗口 B
		// 被排除：非 Anthropic OAuth
		{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeOAuth, SessionWindowStart: &windowA, SessionWindowEnd: ptrTime(windowA.Add(5 * time.Hour)), Extra: map[string]any{"window_cost_limit": 100.0}},
		// 被排除：window_cost_limit <= 0
		oauthWindowAccount(6, windowA, 0),
	}

	// 黄金参考：逐账号实现。
	refRepo := &windowCostBatchRepoStub{standardCost: costs}
	legacy := legacyWindowCostsPerAccount(t, refRepo, accounts)
	require.Equal(t, map[int64]float64{1: 12.5, 2: 0.0, 3: 7.25, 4: 99.0}, legacy,
		"sanity: legacy reference should only include eligible accounts with their StandardCost")

	// 批量实现：必须与逐账号结果逐字节相等。
	batchRepo := &windowCostBatchRepoStub{standardCost: costs}
	svc := &AccountUsageService{usageLogRepo: batchRepo}
	got := svc.GetAccountWindowCostsBatch(context.Background(), accounts)

	require.Equal(t, legacy, got, "batched window costs must equal the per-account result")

	// N+1 → 分桶：两个不同窗口起点 → 恰好两条批量查询，零单查。
	require.Equal(t, int64(2), batchRepo.batchCalls.Load(), "should issue exactly one batch query per distinct window start")
	require.Equal(t, int64(0), batchRepo.singleCalls.Load(), "batch fast-path must not fall back to single queries")
}

func TestGetAccountWindowCostsBatch_FallbackEqualsLegacy(t *testing.T) {
	now := time.Now()
	windowA := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location()).Add(-2 * time.Hour)
	windowB := windowA.Add(-1 * time.Hour)

	costs := map[int64]float64{10: 3.0, 11: 4.0, 12: 5.0}
	accounts := []Account{
		oauthWindowAccount(10, windowA, 100),
		oauthWindowAccount(11, windowA, 100),
		oauthWindowAccount(12, windowB, 100),
	}

	refRepo := &windowCostBatchRepoStub{standardCost: costs}
	legacy := legacyWindowCostsPerAccount(t, refRepo, accounts)

	// 批量查询失败 → 内部回退逐账号单查，结果仍须等价。
	batchRepo := &windowCostBatchRepoStub{standardCost: costs, batchErr: errors.New("boom")}
	svc := &AccountUsageService{usageLogRepo: batchRepo}
	got := svc.GetAccountWindowCostsBatch(context.Background(), accounts)

	require.Equal(t, legacy, got, "fallback path must equal the per-account result")
	require.GreaterOrEqual(t, batchRepo.singleCalls.Load(), int64(3), "fallback must single-query every eligible account")
}

func TestGetAccountWindowCostsBatch_EmptyAndNilSafe(t *testing.T) {
	svc := &AccountUsageService{usageLogRepo: &windowCostBatchRepoStub{standardCost: map[int64]float64{}}}
	require.Empty(t, svc.GetAccountWindowCostsBatch(context.Background(), nil))
	require.Empty(t, svc.GetAccountWindowCostsBatch(context.Background(), []Account{}))

	var nilSvc *AccountUsageService
	require.Empty(t, nilSvc.GetAccountWindowCostsBatch(context.Background(), []Account{oauthWindowAccount(1, time.Now(), 100)}))
}

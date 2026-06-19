package service

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// GetAccountWindowCostsBatch 批量计算多个账号当前窗口内的标准费用（StandardCost）。
//
// 背景：账号列表页（admin /accounts）每行 Anthropic OAuth/SetupToken 账号都要展示
// 当前窗口费用，原实现为每个账号单独发一条聚合 SQL（errgroup，cap 10），账号多时
// 形成 N+1。本方法复用 GatewayService.withWindowCostPrefetch 的规范分桶逻辑：
// 按 GetCurrentWindowStartTime().Unix() 把账号归桶，每个不同窗口起点只跑一条
// GetAccountWindowStatsBatch（ANY($1)）批量查询，把 N 条退化为「不同窗口起点数」条。
//
// 语义与原 per-account 实现严格一致：
//   - 仅纳入 IsAnthropicOAuthOrSetupToken() && GetWindowCostLimit() > 0 的账号；
//   - 返回值为 windowCosts[id] = stats.StandardCost；
//   - 失败开放（partial failure 不报错），缺失账号不写入结果 map（与原 mu.Lock 写入条件一致）。
//
// 当底层仓储未实现批量接口或批量查询失败时，回退到按账号单查（仍失败开放），
// 行为与原 handler 中的 errgroup 循环完全等价。
func (s *AccountUsageService) GetAccountWindowCostsBatch(ctx context.Context, accounts []Account) map[int64]float64 {
	costs := make(map[int64]float64)
	if s == nil || s.usageLogRepo == nil || len(accounts) == 0 {
		return costs
	}

	// 按窗口起点分桶，仅纳入需要查询窗口费用的账号。
	byStart := make(map[int64][]int64)
	startTimes := make(map[int64]time.Time)
	for i := range accounts {
		acc := &accounts[i]
		if !acc.IsAnthropicOAuthOrSetupToken() || acc.GetWindowCostLimit() <= 0 {
			continue
		}
		startTime := acc.GetCurrentWindowStartTime()
		startKey := startTime.Unix()
		byStart[startKey] = append(byStart[startKey], acc.ID)
		startTimes[startKey] = startTime
	}
	if len(byStart) == 0 {
		return costs
	}

	batchReader, hasBatch := s.usageLogRepo.(accountWindowStatsBatchReader)

	// 快路径：批量仓储可用时，每个窗口起点一条 ANY($1) 查询。
	if hasBatch {
		ok := true
		for startKey, ids := range byStart {
			startTime := startTimes[startKey]
			statsByAccount, err := batchReader.GetAccountWindowStatsBatch(ctx, ids, startTime)
			if err != nil {
				ok = false
				break
			}
			for _, id := range ids {
				if stats := statsByAccount[id]; stats != nil {
					costs[id] = stats.StandardCost
				}
			}
		}
		if ok {
			return costs
		}
		// 任一桶批量查询失败 → 整体回退到单查（失败开放）。
		costs = make(map[int64]float64)
	}

	// 回退路径：与原 handler errgroup（cap 10、失败开放）等价。
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(10)
	for startKey, ids := range byStart {
		startTime := startTimes[startKey]
		for _, id := range ids {
			accID := id
			st := startTime
			g.Go(func() error {
				stats, err := s.usageLogRepo.GetAccountWindowStats(gctx, accID, st)
				if err == nil && stats != nil {
					mu.Lock()
					costs[accID] = stats.StandardCost
					mu.Unlock()
				}
				return nil // 不返回错误，允许部分失败
			})
		}
	}
	_ = g.Wait()
	return costs
}

package service

import (
	"context"
	"fmt"
	"time"
)

// GetPassiveUsageBatch 批量构建多个账号的「被动」用量窗口（与
// GET /admin/accounts/:id/usage?source=passive 同源），但把账号读取与窗口统计
// 聚合查询批量化，消除账号列表页逐行扇出的 N+1：
//
//   - accountRepo.GetByIDs 一次取回全部账号（原单查路径每个账号一条 GetByID）；
//   - 对需要窗口统计的 Anthropic OAuth/SetupToken 账号，按 GetCurrentWindowStartTime().Unix()
//     分桶，每个不同窗口起点只跑一条 GetAccountWindowStatsBatch（ANY($1)），结果预热进
//     UsageCache.windowStatsCache（addWindowStats 命中即不再单查）；
//   - 随后对每个账号调用既有的 GetPassiveUsage，保证返回 UsageInfo 与单查路径逐字节一致
//     （含 OpenAI codex 被动重建、Anthropic 7d/7d-Sonnet 子窗、forbidden/ban 标记等）。
//
// 返回 map[accountID]*UsageInfo。无法服务被动用量的账号（如非 OAuth 的 apikey 账号、
// 已删除账号）会被静默跳过，不在结果 map 中——上层据此让对应 cell 显示「-」，与单查
// 路径报错后前端的降级行为一致。
func (s *AccountUsageService) GetPassiveUsageBatch(ctx context.Context, accountIDs []int64) map[int64]*UsageInfo {
	result := make(map[int64]*UsageInfo)
	if s == nil || s.accountRepo == nil || len(accountIDs) == 0 {
		return result
	}

	// 去重，保留首次出现顺序。
	uniqueIDs := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		return result
	}

	accounts, err := s.accountRepo.GetByIDs(ctx, uniqueIDs)
	if err != nil || len(accounts) == 0 {
		return result
	}

	// 预热窗口统计缓存：仅 Anthropic OAuth/SetupToken 的被动路径会调 addWindowStats，
	// 按窗口起点分桶批量查询，把每行一条聚合 SQL 退化为「不同窗口起点数」条。
	s.prefetchWindowStatsForPassive(ctx, accounts)

	for _, account := range accounts {
		if account == nil {
			continue
		}
		// 仅被动用量可服务的账号才纳入（与单查 gate 一致）：Anthropic OAuth/SetupToken、
		// OpenAI OAuth、Kiro 或 Grok。其余（apikey 等）单查会报错，此处跳过让 cell 显示「-」。
		if !account.IsAnthropicOAuthOrSetupToken() && !account.IsOpenAIOAuth() && !account.IsKiro() && !account.IsGrok() {
			continue
		}
		// TK perf: the account is already fully loaded (GetByIDs above), so build
		// the passive usage straight from it instead of GetPassiveUsage(ctx, ID),
		// which would re-issue a per-account GetByID (a PK lookup + a groups join)
		// for data we already hold. With the window-stats cache prewarmed above,
		// that GetByID was the last residual per-account DB round-trip in this
		// fan-out. See getPassiveUsageForAccount.
		usage, perr := s.getPassiveUsageForAccount(ctx, account)
		if perr != nil || usage == nil {
			continue
		}
		result[account.ID] = usage
	}
	return result
}

// getPassiveUsageForAccount builds the passive UsageInfo from an already-loaded
// *Account, skipping the GetByID that GetPassiveUsage(ctx, accountID) performs.
//
// It MUST stay byte-identical to GetPassiveUsage's body after its GetByID
// (account_usage_service.go). GetPassiveUsage is an upstream-owned method, so
// rather than refactor it to delegate here (an upstream edit), the post-fetch
// logic is mirrored in this TK companion. Drift is caught mechanically by
// TestGetPassiveUsageBatch_EqualsSinglePerAccount, which asserts this batch
// path (now routed through getPassiveUsageForAccount) matches the per-account
// GetPassiveUsage output.
func (s *AccountUsageService) getPassiveUsageForAccount(ctx context.Context, account *Account) (*UsageInfo, error) {
	if account == nil {
		return nil, fmt.Errorf("get account failed: nil account")
	}

	// OpenAI OAuth (codex): rebuilt from Extra's codex_*_used_percent passive
	// sampling, never probing upstream — same source as GetPassiveUsage.
	if account.IsOpenAIOAuth() {
		return s.buildPassiveOpenAIUsage(account), nil
	}

	// Kiro: rebuilt from Extra's kiro_usage_* passive sampling, never probing
	// upstream — mirrors GetPassiveUsage's kiro branch.
	if account.IsKiro() {
		return s.buildPassiveKiroUsage(account), nil
	}

	// Grok/xAI has no upstream percentage quota API; expose local 5h/7d billing
	// windows through the same passive batch path the account list already owns.
	if account.IsGrok() {
		return s.buildLocalWindowUsage(ctx, account), nil
	}

	if !account.IsAnthropicOAuthOrSetupToken() {
		return nil, fmt.Errorf("passive usage only supported for Anthropic OAuth/SetupToken accounts")
	}

	info := s.estimateSetupTokenUsage(account)
	info.Source = "passive"
	info.UpdatedAt = parseExtraSampledAt(account.Extra["passive_usage_sampled_at"])
	info.SevenDay = buildPassiveWindow(account.Extra, "passive_usage_7d_utilization", "passive_usage_7d_reset")
	info.SevenDaySonnet = buildPassiveWindow(account.Extra, "passive_usage_7d_sonnet_utilization", "passive_usage_7d_sonnet_reset")
	s.addWindowStats(ctx, account, info)

	return info, nil
}

// prefetchWindowStatsForPassive 把被动路径需要的窗口统计批量查询并写入
// UsageCache.windowStatsCache，使随后的 GetPassiveUsage→addWindowStats 命中缓存、
// 不再逐账号单查。仅纳入 Anthropic OAuth/SetupToken 账号（OpenAI 被动路径
// buildPassiveOpenAIUsage 不读窗口统计）。失败开放：任何查询失败只是少预热几个
// 账号，后续单查会自然回填。
func (s *AccountUsageService) prefetchWindowStatsForPassive(ctx context.Context, accounts []*Account) {
	if s.usageLogRepo == nil || s.cache == nil {
		return
	}
	batchReader, hasBatch := s.usageLogRepo.(accountWindowStatsBatchReader)
	if !hasBatch {
		return // 无批量能力时不预热，交由单查路径（语义不变，只是少一层优化）
	}

	byStart := make(map[int64][]int64)
	startTimes := make(map[int64]time.Time)
	for _, account := range accounts {
		if account == nil || !account.IsAnthropicOAuthOrSetupToken() {
			continue
		}
		// 已有未过期缓存的账号无需重复查询。
		if cached, ok := s.cache.windowStatsCache.Load(account.ID); ok {
			if c, ok := cached.(*windowStatsCache); ok && time.Since(c.timestamp) < windowStatsCacheTTL {
				continue
			}
		}
		startTime := account.GetCurrentWindowStartTime()
		startKey := startTime.Unix()
		byStart[startKey] = append(byStart[startKey], account.ID)
		startTimes[startKey] = startTime
	}
	if len(byStart) == 0 {
		return
	}

	now := time.Now()
	for startKey, ids := range byStart {
		startTime := startTimes[startKey]
		statsByAccount, err := batchReader.GetAccountWindowStatsBatch(ctx, ids, startTime)
		if err != nil {
			continue // 失败开放：该桶不预热，单查回填
		}
		for _, id := range ids {
			ws := &WindowStats{}
			if stats := statsByAccount[id]; stats != nil {
				ws = &WindowStats{
					Requests:     stats.Requests,
					Tokens:       stats.Tokens,
					Cost:         stats.Cost,
					StandardCost: stats.StandardCost,
					UserCost:     stats.UserCost,
				}
			}
			s.cache.windowStatsCache.Store(id, &windowStatsCache{
				stats:     ws,
				timestamp: now,
			})
		}
	}
}

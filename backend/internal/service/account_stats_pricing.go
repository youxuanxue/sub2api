package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// resolveAccountStatsCost 计算账号统计定价费用。
// 返回 nil 表示不覆盖，使用默认公式（total_cost × account_rate_multiplier）。
//
// 优先级（先命中为准）：
//  1. 自定义规则（始终尝试，不依赖 ApplyPricingToAccountStats 开关）
//  2. ApplyPricingToAccountStats 启用时，直接使用本次请求的客户计费（倍率前的 totalCost）
//  3. 镜像用户 token 计费（倍率=1，与用户 CalculateCostUnified 同路径）— 优先于文件价
//  4. 模型定价文件（LiteLLM）默认价格 — 经 calculateCostInternalWithPolicyAt / computeTokenBreakdown
//  5. nil → 走默认公式（total_cost × account_rate_multiplier）
//
// NOTE: 2026-09-02 之前 priority-3 路径未应用峰谷价，历史 usage_logs.account_stats_cost
// 在峰时 DeepSeek 流量上可能约为 total_cost 的一半；不做 backfill，仅 forward-fix。
//
// upstreamModel 是最终发往上游的模型 ID。
// totalCost 是本次请求的客户计费（倍率前），用于优先级 2。
// serviceTier 是最终参与用户计费的 OpenAI 服务层级，用于优先级 3。
// billingAt 与用户计费的 BillingAt 对齐（通常为 recordUsageBillingAt(pricingAt)）。
// mirroredTokenCostAtMultOne 为用户 token 计费路径在倍率=1 下的 totalCost；非 nil 且 >0 时用于优先级 3。
func resolveAccountStatsCost(
	ctx context.Context,
	channelService *ChannelService,
	billingService *BillingService,
	accountID int64,
	groupID int64,
	upstreamModel string,
	tokens UsageTokens,
	requestCount int,
	totalCost float64,
	serviceTier string,
	billingAt time.Time,
	mirroredTokenCostAtMultOne *float64,
) *float64 {
	if channelService == nil || upstreamModel == "" {
		return nil
	}
	channel, err := channelService.GetChannelForGroup(ctx, groupID)
	if err != nil || channel == nil {
		return nil
	}

	platform := channelService.GetGroupPlatform(ctx, groupID)

	// 优先级 1：自定义规则（始终尝试）
	if cost := tryCustomRules(channel, accountID, groupID, platform, upstreamModel, tokens, requestCount); cost != nil {
		return cost
	}

	// 优先级 2：渠道开启"应用模型定价到账号统计"时，直接使用客户计费（倍率前）
	if channel.ApplyPricingToAccountStats {
		cost := totalCost
		if cost <= 0 {
			return nil
		}
		return &cost
	}

	// 优先级 3：镜像用户 token 计费（倍率=1），与用户 CalculateCostUnified 同定价源
	if mirroredTokenCostAtMultOne != nil && *mirroredTokenCostAtMultOne > 0 {
		cost := *mirroredTokenCostAtMultOne
		return &cost
	}

	// 优先级 4：模型定价文件（LiteLLM）— 与用户 total_cost 共用 computeTokenBreakdown
	if billingService != nil {
		return tryModelFilePricing(billingService, upstreamModel, tokens, serviceTier, billingAt)
	}

	return nil
}

// usageLogAccountStatsTokens derives billing tokens from the persisted usage log
// fields so account stats see the same token breakdown as user settlement.
func usageLogAccountStatsTokens(log *UsageLog) UsageTokens {
	if log == nil {
		return UsageTokens{}
	}
	return UsageTokens{
		InputTokens:           log.InputTokens,
		OutputTokens:          log.OutputTokens,
		CacheCreationTokens:   log.CacheCreationTokens,
		CacheReadTokens:       log.CacheReadTokens,
		CacheCreation5mTokens: log.CacheCreation5mTokens,
		CacheCreation1hTokens: log.CacheCreation1hTokens,
		ImageOutputTokens:     log.ImageOutputTokens,
		ImageInputTokens:      log.ImageInputTokens,
	}
}

// tryModelFilePricing prices account stats from the LiteLLM/fallback registry via
// calculateCostInternalWithPolicyAt (computeTokenBreakdown SSOT).
func tryModelFilePricing(billingService *BillingService, model string, tokens UsageTokens, serviceTier string, billingAt time.Time) *float64 {
	pricing, err := billingService.GetModelPricing(model)
	if err != nil || pricing == nil {
		return nil
	}
	at := RecordUsageBillingAt(billingAt)
	longCtx := billingService.shouldApplySessionLongContextPricing(tokens, pricing)
	breakdown, err := billingService.calculateCostInternalWithPolicyAt(
		model, tokens, 1, normalizeBillingServiceTier(serviceTier), nil, longCtx, at,
	)
	if err != nil || breakdown == nil || breakdown.TotalCost <= 0 {
		return nil
	}
	return &breakdown.TotalCost
}

// tryCustomRules 遍历自定义规则，按数组顺序先命中为准。
func tryCustomRules(
	channel *Channel, accountID, groupID int64,
	platform, model string, tokens UsageTokens, requestCount int,
) *float64 {
	modelLower := strings.ToLower(model)
	for _, rule := range channel.AccountStatsPricingRules {
		if !matchAccountStatsRule(&rule, accountID, groupID) {
			continue
		}
		pricing := findPricingForModel(rule.Pricing, platform, modelLower)
		if pricing == nil {
			continue // 规则匹配但模型不在规则定价中，继续下一条
		}
		return calculateStatsCost(pricing, tokens, requestCount)
	}
	return nil
}

// matchAccountStatsRule 检查规则是否匹配指定的 accountID 和 groupID。
// 匹配条件：accountID ∈ rule.AccountIDs 或 groupID ∈ rule.GroupIDs。
// 如果规则的 AccountIDs 和 GroupIDs 都为空，视为不匹配。
func matchAccountStatsRule(rule *AccountStatsPricingRule, accountID, groupID int64) bool {
	if len(rule.AccountIDs) == 0 && len(rule.GroupIDs) == 0 {
		return false
	}
	for _, id := range rule.AccountIDs {
		if id == accountID {
			return true
		}
	}
	for _, id := range rule.GroupIDs {
		if id == groupID {
			return true
		}
	}
	return false
}

// findPricingForModel 在定价列表中查找匹配的模型定价。
// 先精确匹配，再通配符匹配（按配置顺序，先匹配先使用）。
func findPricingForModel(pricingList []ChannelModelPricing, platform, modelLower string) *ChannelModelPricing {
	// 精确匹配优先
	for i := range pricingList {
		p := &pricingList[i]
		if !isPlatformMatch(platform, p.Platform) {
			continue
		}
		for _, m := range p.Models {
			if strings.ToLower(m) == modelLower {
				return p
			}
		}
	}
	// 通配符匹配：按配置顺序，先匹配先使用
	for i := range pricingList {
		p := &pricingList[i]
		if !isPlatformMatch(platform, p.Platform) {
			continue
		}
		for _, m := range p.Models {
			ml := strings.ToLower(m)
			if !strings.HasSuffix(ml, "*") {
				continue
			}
			prefix := strings.TrimSuffix(ml, "*")
			if strings.HasPrefix(modelLower, prefix) {
				return p
			}
		}
	}
	return nil
}

// isPlatformMatch 判断平台是否匹配（空平台视为不限平台）。
func isPlatformMatch(queryPlatform, pricingPlatform string) bool {
	if queryPlatform == "" || pricingPlatform == "" {
		return true
	}
	return queryPlatform == pricingPlatform
}

// calculateStatsCost 使用给定的定价计算费用（不含任何倍率，原始费用）。
func calculateStatsCost(pricing *ChannelModelPricing, tokens UsageTokens, requestCount int) *float64 {
	if pricing == nil {
		return nil
	}
	switch pricing.BillingMode {
	case BillingModePerRequest, BillingModeImage:
		return calculatePerRequestStatsCost(pricing, requestCount)
	default:
		return calculateTokenStatsCost(pricing, tokens)
	}
}

// calculatePerRequestStatsCost 按次/图片计费。
func calculatePerRequestStatsCost(pricing *ChannelModelPricing, requestCount int) *float64 {
	if pricing.PerRequestPrice == nil || *pricing.PerRequestPrice <= 0 {
		return nil
	}
	cost := *pricing.PerRequestPrice * float64(requestCount)
	return &cost
}

// calculateTokenStatsCost Token 计费。
// If the pricing has intervals, find the matching interval by total token count
// and use its prices instead of the flat pricing fields.
func calculateTokenStatsCost(pricing *ChannelModelPricing, tokens UsageTokens) *float64 {
	p := pricing
	if len(pricing.Intervals) > 0 {
		totalTokens := tokens.InputTokens + tokens.OutputTokens + tokens.CacheCreationTokens + tokens.CacheReadTokens
		if iv := FindMatchingInterval(pricing.Intervals, totalTokens); iv != nil {
			p = &ChannelModelPricing{
				InputPrice:      iv.InputPrice,
				OutputPrice:     iv.OutputPrice,
				CacheWritePrice: iv.CacheWritePrice,
				CacheReadPrice:  iv.CacheReadPrice,
				PerRequestPrice: iv.PerRequestPrice,
			}
		}
	}
	deref := func(ptr *float64) float64 {
		if ptr == nil {
			return 0
		}
		return *ptr
	}
	cost := float64(tokens.InputTokens)*deref(p.InputPrice) +
		float64(tokens.OutputTokens)*deref(p.OutputPrice) +
		float64(tokens.CacheCreationTokens)*deref(p.CacheWritePrice) +
		float64(tokens.CacheReadTokens)*deref(p.CacheReadPrice) +
		float64(tokens.ImageOutputTokens)*deref(p.ImageOutputPrice)
	if cost <= 0 {
		return nil
	}
	return &cost
}

// applyAccountStatsCost resolves the account stats cost for a usage log entry.
// billingAt must match the BillingAt passed to user CalculateCostUnified on the
// same record (typically RecordUsageBillingAt(pricingAt)).
func applyAccountStatsCost(
	ctx context.Context,
	usageLog *UsageLog,
	cs *ChannelService, bs *BillingService,
	accountID int64, groupID int64,
	upstreamModel, requestedModel string,
	totalCost float64,
	billingAt time.Time,
	mirroredTokenCostAtMultOne *float64,
) {
	model := upstreamModel
	if model == "" {
		model = requestedModel
	}
	requestCount := 1
	if usageLog != nil && usageLog.ImageCount > 0 {
		requestCount = usageLog.ImageCount
	}
	serviceTier := ""
	if usageLog != nil && usageLog.ServiceTier != nil {
		serviceTier = *usageLog.ServiceTier
	}
	at := billingAt
	if at.IsZero() && usageLog != nil && !usageLog.CreatedAt.IsZero() {
		at = usageLog.CreatedAt
	}
	if at.IsZero() {
		at = timezone.Now()
	}
	usageLog.AccountStatsCost = resolveAccountStatsCost(
		ctx, cs, bs, accountID, groupID, model, usageLogAccountStatsTokens(usageLog),
		requestCount, totalCost, serviceTier, at, mirroredTokenCostAtMultOne,
	)
}

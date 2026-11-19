//go:build unit

package service

import (
	"context"
	"time"
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
// mirroredTokenCostAtMultOne 为用户 token 计费路径在倍率=1 下的 totalCost；非 nil 时用于优先级 3（含明确零价）。
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
	reasoningEfforts ...string,
) *float64 {
	return resolveAccountStatsCostWithUpstreamFactor(ctx, channelService, billingService, accountID, groupID, upstreamModel, tokens, requestCount, totalCost, serviceTier, billingAt, mirroredTokenCostAtMultOne, 1, reasoningEfforts...)
}

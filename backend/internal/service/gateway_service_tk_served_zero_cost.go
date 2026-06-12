package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// TK 根因②：「已服务但零计费」统一探针。
//
// 背景：计价不确定时系统选择"免费放行"（unpriced never blocks），漏血默认无声。
// 除无价模型（ErrModelPricingUnavailable，已有错误侧观测）外，还有多类静默 $0：
// 非缺价 cost-calc 错误吞 $0、渠道/视频/per_request 无价、负倍率归零。本探针在两条
// 计费 funnel 的记账点（cost 已知后）统一按**结果**判定（TotalCost/ActualCost），
// 一张网罩住所有静默路径，命中即经 PricingMissingNotifier 发 P0 告警 + 结构化日志。
// 不拒绝服务、不改金额，纯可观测性。判据见 tkServedZeroCostReason。

// tkServedZeroCostReason 判定一次已记账请求是否构成"计费漏算"并给出原因。
// billableUnits<=0（失败/空请求/count_tokens 不记账）→ 不报。
//   - TotalCost==0（倍率前价格本身为零）→ "unpriced"：无价模型 / 渠道 $0 /
//     视频·per_request 无价 / cost-calc 错误返回的零 CostBreakdown。
//   - TotalCost>0 但 ActualCost==0 且倍率<0 → "negative_multiplier"：价格有效却被
//     负倍率归零。倍率恰为 0 是合法免费分组，**不报**。
func tkServedZeroCostReason(cost *CostBreakdown, billableUnits int64, multiplier, accountRateMultiplier float64) (string, bool) {
	if cost == nil || billableUnits <= 0 {
		return "", false
	}
	if cost.TotalCost == 0 {
		return "unpriced", true
	}
	if cost.ActualCost == 0 && (multiplier < 0 || accountRateMultiplier < 0) {
		return "negative_multiplier", true
	}
	return "", false
}

// tkClaudeUsageBillableUnits 估算计费单元：token 总量优先，回退图片张数。
func tkClaudeUsageBillableUnits(u ClaudeUsage, imageCount int) int64 {
	t := int64(u.InputTokens) + int64(u.OutputTokens) +
		int64(u.CacheCreationInputTokens) + int64(u.CacheReadInputTokens) +
		int64(u.ImageOutputTokens)
	if t > 0 {
		return t
	}
	if imageCount > 0 {
		return int64(imageCount)
	}
	return 0
}

// tkNotifyServedZeroCost 是 anthropic 计费 funnel（recordUsageCore）的薄注入：
// cost 已知后判定，命中即发 P0 告警 + 结构化日志。nil-safe，发送异步，绝不阻塞
// funnel。注：Feishu 关闭时 notifier 自然 no-op，但日志始终在（ops 可 grep）。
func (s *GatewayService) tkNotifyServedZeroCost(
	cost *CostBreakdown, result *ForwardResult, apiKey *APIKey,
	billingModel, requestedModel string, multiplier, accountRateMultiplier float64,
) {
	if s == nil || cost == nil || result == nil {
		return
	}
	units := tkClaudeUsageBillableUnits(result.Usage, result.ImageCount)
	reason, ok := tkServedZeroCostReason(cost, units, multiplier, accountRateMultiplier)
	if !ok {
		return
	}

	fields := []zap.Field{
		zap.String("component", "service.gateway"),
		zap.String("reason", reason),
		zap.String("billing_model", billingModel),
		zap.String("requested_model", requestedModel),
		zap.String("upstream_model", result.UpstreamModel),
		zap.Int64("billable_units", units),
		zap.Float64("total_cost", cost.TotalCost),
		zap.Float64("rate_multiplier", multiplier),
		zap.Float64("account_rate_multiplier", accountRateMultiplier),
	}
	if apiKey != nil {
		fields = append(fields, zap.Int64("api_key_id", apiKey.ID))
		if apiKey.Group != nil {
			fields = append(fields,
				zap.Int64("group_id", apiKey.Group.ID),
				zap.String("group_platform", apiKey.Group.Platform),
			)
		}
	}
	logger.L().With(fields...).Warn("gateway_usage.served_zero_cost")

	if s.tkPricingMissingNotifier == nil {
		return
	}
	ev := PricingMissingEvent{
		Reason:         reason,
		BillingModel:   billingModel,
		RequestedModel: requestedModel,
		UpstreamModel:  result.UpstreamModel,
		Tokens:         units,
	}
	if apiKey != nil {
		ev.APIKeyID = apiKey.ID
		if apiKey.Group != nil {
			ev.GroupID = apiKey.Group.ID
			ev.GroupName = apiKey.Group.Name
			ev.Platform = apiKey.Group.Platform
		}
	}
	s.tkPricingMissingNotifier.NotifyPricingMissing(ev)
}

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

// tkServedAtFallbackReason 是「按家族 floor 服务」的 PricingMissingNotifier reason（设计转向后的
// 收敛信号：cost>0 但走的是 Go 家族 floor 而非真价，需补真价）。区别于 served_zero_cost 的 $0。
const tkServedAtFallbackReason = "served_at_fallback"

// tkNotifyServedAtFallback 触发「按家族 floor 服务」收敛告警：本次请求按 **非 $0 的 Go 家族 floor**
// 计费（而非 litellm/overlay 真价），应补真价让 fallback 用量衰减到稳态（docs §4 收敛引擎）。两条
// 计费 funnel（anthropic recordUsageCore + openai）共用。与 served_zero_cost（cost==0）互斥。
// nil-safe，异步发送，绝不阻塞 funnel。
//
// 已知 narrow 误报（对抗复审 S1，接受为观测噪声、不影响计费）：调用方【未】跳过 channel 计费请求，
// 而 IsServedViaFamilyFloor 不查渠道价 —— 故一个【基础价缺、但配了渠道价、且名字含 gemini/gpt/claude】
// 的模型（按真实渠道价计费）会被误判为「走 floor」误发一张卡。这类很窄（渠道价多在无 floor 的 newapi），
// 运维一眼可辨（该模型有渠道价、无需补），与 openai 多候选误报（N2）同性质。真正需补真价的「无渠道价、
// 走 Go floor」是主路径，不漏报。
func tkNotifyServedAtFallback(
	notifier PricingMissingNotifier, billing *BillingService, cost *CostBreakdown,
	apiKey *APIKey, billingModel, requestedModel, upstreamModel string, units int64,
) {
	if billing == nil || cost == nil || units <= 0 || cost.TotalCost <= 0 {
		return // cost<=0 归 served_zero_cost；无计费单元 → 跳过
	}
	if !billing.IsServedViaFamilyFloor(billingModel) {
		return // 有真价（或无 floor → 已被闸拒）→ 非「按 floor 服务」
	}
	fields := []zap.Field{
		zap.String("component", "service.gateway"),
		zap.String("reason", tkServedAtFallbackReason),
		zap.String("billing_model", billingModel),
		zap.String("requested_model", requestedModel),
		zap.String("upstream_model", upstreamModel),
		zap.Int64("billable_units", units),
		zap.Float64("total_cost", cost.TotalCost),
	}
	if apiKey != nil {
		fields = append(fields, zap.Int64("api_key_id", apiKey.ID))
		if apiKey.Group != nil {
			fields = append(fields, zap.Int64("group_id", apiKey.Group.ID), zap.String("group_platform", apiKey.Group.Platform))
		}
	}
	logger.L().With(fields...).Warn("gateway_usage.served_at_fallback")

	if notifier == nil {
		return
	}
	ev := PricingMissingEvent{
		Reason:         tkServedAtFallbackReason,
		BillingModel:   billingModel,
		RequestedModel: requestedModel,
		UpstreamModel:  upstreamModel,
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
	notifier.NotifyPricingMissing(ev)
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

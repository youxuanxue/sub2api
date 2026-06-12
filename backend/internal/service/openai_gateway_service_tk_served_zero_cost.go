package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// TK 根因②：openai 计费 funnel（OpenAIGatewayService.RecordUsage）的「已服务但
// 零计费」薄注入。判据与告警同 anthropic 侧（gateway_service_tk_served_zero_cost.go
// 的 tkServedZeroCostReason）。actualInputTokens 由调用点传入（openai 路径的输入
// token 已剔除 cache_read，见 RecordUsage）。
func (s *OpenAIGatewayService) tkNotifyServedZeroCost(
	cost *CostBreakdown, result *OpenAIForwardResult, apiKey *APIKey,
	input *OpenAIRecordUsageInput, billingModels []string,
	actualInputTokens int, multiplier, accountRateMultiplier float64,
) {
	if s == nil || cost == nil || result == nil {
		return
	}
	units := int64(actualInputTokens) +
		int64(result.Usage.OutputTokens) +
		int64(result.Usage.CacheCreationInputTokens) +
		int64(result.Usage.CacheReadInputTokens) +
		int64(result.Usage.ImageOutputTokens)
	if units <= 0 && result.ImageCount > 0 {
		units = int64(result.ImageCount)
	}
	reason, ok := tkServedZeroCostReason(cost, units, multiplier, accountRateMultiplier)
	if !ok {
		return
	}

	requested := result.Model
	if input != nil && input.OriginalModel != "" {
		requested = input.OriginalModel
	}
	billingModel := firstUsageBillingModel(billingModels)

	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.String("reason", reason),
		zap.String("billing_model", billingModel),
		zap.String("requested_model", requested),
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
	logger.L().With(fields...).Warn("openai_usage.served_zero_cost")

	if s.tkPricingMissingNotifier == nil {
		return
	}
	ev := PricingMissingEvent{
		Reason:         reason,
		BillingModel:   billingModel,
		RequestedModel: requested,
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

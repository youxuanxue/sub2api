package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// logTokenCostPricingMissing emits a structured warning when token-cost
// calculation fails, mirroring the OpenAI record-usage path's
// "openai_usage.pricing_missing_record_zero_cost" observability.
//
// TK (upstream Wei-Shaw/sub2api#1833 / #1544): GatewayService.calculateTokenCost
// previously logged any cost-calculation failure via logger.LegacyPrintf and
// returned &CostBreakdown{ActualCost: 0}. For a model with no LiteLLM pricing
// entry and no channel pricing configured — e.g. GLM / qwen / deepseek attached
// to an Anthropic-type group and driven over /v1/messages (the opencode shape) —
// that silently billed zero, i.e. free usage = revenue leak, with no signal for
// the operator to notice and configure pricing. The OpenAI record-usage path
// already distinguishes pricing-missing (observable zero-cost) from other
// failures; this brings the Anthropic / generic token-cost path to parity so the
// zero-cost case is observable rather than silent.
func logTokenCostPricingMissing(billingModel string, apiKey *APIKey, result *ForwardResult, err error) {
	fields := []zap.Field{
		zap.String("component", "service.gateway"),
		zap.String("billing_model", billingModel),
		zap.Error(err),
	}
	if result != nil {
		fields = append(fields,
			zap.String("requested_model", result.Model),
			zap.String("upstream_model", result.UpstreamModel),
		)
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

	if isUsagePricingUnavailableError(err) {
		logger.L().With(fields...).Warn("gateway_usage.pricing_missing_record_zero_cost")
		return
	}
	logger.L().With(fields...).Warn("gateway_usage.cost_calculation_failed_record_zero_cost")
}

// SetPricingMissingNotifier wires the pricing-missing Feishu notifier
// post-construction (TK companion setter — same shape as
// SetPricingAvailabilityService) so the upstream constructor signature stays
// unchanged. nil = feature disabled.
func (s *GatewayService) SetPricingMissingNotifier(n PricingMissingNotifier) {
	if s == nil {
		return
	}
	s.tkPricingMissingNotifier = n
}

// Note: the former recordTokenCostPricingMissing (error-side log + Feishu notify)
// has been consolidated. The structured log is now emitted directly via
// logTokenCostPricingMissing at the calculateTokenCost error branch, and the
// Feishu P0 alert moved to the result-side "served but zero cost" probe
// (tkNotifyServedZeroCost / tkServedZeroCostReason in
// gateway_service_tk_served_zero_cost.go), which also catches the silent $0
// paths that never return an error.

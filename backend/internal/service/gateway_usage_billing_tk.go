package service

import "context"

// tkRecordUsagePostCostObservability runs TokenKey-only post-cost probes
// (served-but-zero-cost + family-floor fallback) before the usage log is built.
func (s *GatewayService) tkRecordUsagePostCostObservability(
	cost *CostBreakdown,
	result *ForwardResult,
	apiKey *APIKey,
	billingModel, requestedModel string,
	multiplier, accountRateMultiplier float64,
) {
	s.tkNotifyServedZeroCost(cost, result, apiKey, billingModel, requestedModel, multiplier, accountRateMultiplier)
	tkNotifyServedAtFallback(s.tkPricingMissingNotifier, s.billingService, cost, apiKey, billingModel, requestedModel, result.UpstreamModel, tkClaudeUsageBillableUnits(result.Usage, result.ImageCount))
}

// tkRecordAvailabilitySuccessOutcome records a successful forward outcome on
// the pricing-availability evidence owner (passive observability).
func (s *GatewayService) tkRecordAvailabilitySuccessOutcome(ctx context.Context, account *Account, result *ForwardResult) {
	if s.tkPricingAvailability == nil || account == nil || result == nil {
		return
	}
	s.tkPricingAvailability.RecordOutcome(ctx, AvailabilityOutcome{
		Platform:           account.Platform,
		ModelID:            result.UpstreamModel,
		AccountID:          account.ID,
		Success:            true,
		UpstreamStatusCode: 200,
	})
}

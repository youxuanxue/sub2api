package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

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

// Older media/non-protocol callers still report success from billing. Results
// already observed at the attempt boundary (including partial failures) skip it.
func (s *GatewayService) tkRecordAvailabilitySuccessOutcome(ctx context.Context, account *Account, result *ForwardResult) {
	if result == nil || result.availabilityObserved {
		return
	}
	s.TKRecordProtocolOutcome(ctx, account, protocolrouter.Plan{}, result.UpstreamModel, result, nil)
}

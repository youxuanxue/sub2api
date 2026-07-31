package service

import (
	"context"
	"strings"
)

// isOpenAIVideoUsageResult reports video-generation settlement inputs.
// VideoCount>0 is stamped by VideoSubmit (seedance/veo/newapi bridge) and Grok
// native /videos/* handlers. The prior grok-model-name gate dropped non-grok
// async video submits into the token pricing funnel → pricing_missing_record_zero_cost.
func isOpenAIVideoUsageResult(result *OpenAIForwardResult) bool {
	return result != nil && result.VideoCount > 0
}

func isGrokVideoBillingModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "grok-imagine-video")
}

// tkTryCalculateOpenAIVideoUsageCost routes per-second video settlement when
// applicable. Returns (cost, true) when the video path owns billing; (nil, false)
// when the caller should fall through (e.g. grok + channel token pricing).
func (s *OpenAIGatewayService) tkTryCalculateOpenAIVideoUsageCost(
	ctx context.Context,
	result *OpenAIForwardResult,
	apiKey *APIKey,
	billingModels []string,
	videoMultiplier float64,
) (*CostBreakdown, bool) {
	if !isOpenAIVideoUsageResult(result) {
		return nil, false
	}
	billingModel := firstUsageBillingModel(billingModels)
	resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey)
	if resolved != nil && resolved.Mode == BillingModeToken {
		if isGrokVideoBillingModel(billingModel) {
			return nil, false
		}
		resolution := NormalizeVideoBillingResolutionForModel(billingModel, result.VideoResolution)
		if _, ok := tkVideoUnitPriceUSD(billingModel, resolution, videoBillingOptionsFromForwardResult(result)); !ok {
			return nil, false
		}
	}
	return s.calculateOpenAIVideoCost(ctx, billingModel, apiKey, result, videoMultiplier), true
}

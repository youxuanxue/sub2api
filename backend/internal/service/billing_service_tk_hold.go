package service

import "strings"

// TK: pre-flight HOLD estimates. These helpers price the caller-provided
// reserve inputs using conservative unit prices. Token holds are a true upper
// bound only when the caller passes a real output ceiling; callers may
// deliberately pass a lower default reserve for omitted ceilings to avoid
// rejecting ordinary traffic on stale low-balance snapshots. Exact settlement
// remains the source of truth for the final bill.

// EstimateTokenHold returns the reserve amount for a token-billed request
// (chat / responses / messages; embeddings pass maxOutputTokens=0).
//
// Conservative construction vs. computeTokenBreakdown's actual formula:
//   - input side: every prompt token is priced at the MAX of all input-side
//     unit prices (input / cache-creation 5m / 1h / priority). At billing time
//     each token is one of input | cache_read | cache_creation; cache_read is
//     the cheapest and cache_creation the dearest, so max() dominates any split.
//   - output side: the caller-provided maxOutputTokens at the MAX of output /
//     priority-output / image-output unit price. This is a hard upper bound
//     only when maxOutputTokens came from an explicit client ceiling.
//   - long-context: applied whenever promptTokens COULD cross the model
//     threshold (actual triggers on input+cache_read ≤ promptTokens, so this is
//     a safe over-approximation).
//   - service tier: serviceTierCostMultiplier covers the non-priority-unit
//     branch (priority→2.0); combined with the priority unit price in max()
//     above it over-covers the priority branch (computeTokenBreakdown picks
//     ONE of the two), never under.
func (s *BillingService) EstimateTokenHold(model, serviceTier string, promptTokens, maxOutputTokens int, rateMultiplier float64) (float64, error) {
	pricing, err := s.GetModelPricing(model) // already carries long-context policy
	if err != nil {
		return 0, err
	}
	if promptTokens < 0 {
		promptTokens = 0
	}
	if maxOutputTokens < 0 {
		maxOutputTokens = 0
	}

	unitIn := maxFloat(
		pricing.InputPricePerToken,
		pricing.InputPricePerTokenPriority,
		pricing.CacheCreationPricePerToken,
		pricing.CacheCreation5mPrice,
		pricing.CacheCreation1hPrice,
	)
	unitOut := maxFloat(
		pricing.OutputPricePerToken,
		pricing.OutputPricePerTokenPriority,
		pricing.ImageOutputPricePerToken,
	)

	lcIn, lcOut := 1.0, 1.0
	if pricing.LongContextInputThreshold > 0 && promptTokens > pricing.LongContextInputThreshold {
		if pricing.LongContextInputMultiplier > 1 {
			lcIn = pricing.LongContextInputMultiplier
		}
		if pricing.LongContextOutputMultiplier > 1 {
			lcOut = pricing.LongContextOutputMultiplier
		}
	}

	tier := serviceTierCostMultiplier(serviceTier)
	if rateMultiplier < 0 {
		rateMultiplier = 0
	}

	total := float64(promptTokens)*unitIn*lcIn + float64(maxOutputTokens)*unitOut*lcOut
	return total * tier * rateMultiplier, nil
}

// EstimateImageHold returns an upper bound on an image-generation request:
// CalculateImageCost over the REQUESTED image count (actual delivers ≤ n) at
// the requested size tier. An empty/unknown size tier is priced as 4K (the
// dearest tier, 2× base) so a request that omits size cannot under-reserve.
func (s *BillingService) EstimateImageHold(model, sizeTier string, n int, groupConfig *ImagePriceConfig, rateMultiplier float64) float64 {
	if n <= 0 {
		n = 1
	}
	if strings.TrimSpace(sizeTier) == "" {
		sizeTier = "4K"
	}
	bd := s.CalculateImageCost(model, sizeTier, n, groupConfig, rateMultiplier)
	if bd == nil {
		return 0
	}
	return bd.ActualCost
}

// EstimateVideoHold returns an upper bound on an async video submit:
// CalculateVideoCost over the requested duration (the same seconds actual bills,
// so this is exact). Callers pass the request-clamped seconds (handlers clamp
// to [1,60]).
func (s *BillingService) EstimateVideoHold(model string, seconds int64, rateMultiplier float64) float64 {
	bd := s.CalculateVideoCost(model, VideoBillingResolution720P, 1, int(seconds), nil, rateMultiplier)
	if bd == nil {
		return 0
	}
	return bd.ActualCost
}

// maxFloat returns the largest of the given values (0 for an empty list).
func maxFloat(vs ...float64) float64 {
	m := 0.0
	for _, v := range vs {
		if v > m {
			m = v
		}
	}
	return m
}

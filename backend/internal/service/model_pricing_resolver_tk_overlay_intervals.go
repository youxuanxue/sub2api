package service

// TK: input-token interval (tiered) pricing sourced from the curated overlay
// (tk_pricing_overlay.json "intervals"), not just channel DB pricing.
//
// Why: DashScope / Alibaba list several models with whole-request tier pricing
// keyed on input-token count (qwen3.7-plus / qwen3.6-flash at 256K; qwen3-coder-plus
// at 32K/128K/256K). The billing engine already bills such tiers via
// ResolvedPricing.Intervals (calculateTokenCost -> GetIntervalPricing ->
// FindMatchingInterval, whole-request, input-context-keyed — an exact match for the
// DashScope model). That slot was previously populated ONLY by channel pricing
// (applyTokenOverrides). This lets the version-controlled overlay carry tiers too,
// so tiered third-party models can be git-configured (PR-reviewed, reproducible
// across envs) instead of requiring hand-set DB channel pricing.
//
// Precedence is unchanged: channel pricing still wins. tkApplyOverlayIntervals only
// fills resolved.Intervals when no channel interval override is present.

// tkApplyOverlayIntervals promotes overlay-defined intervals (carried on
// BasePricing.Intervals via the overlay loader) into ResolvedPricing.Intervals,
// but only for token mode and only when channel pricing has not already supplied
// intervals. The flat BasePricing fields remain the out-of-range fallback that
// GetIntervalPricing overlays each matched interval onto.
func tkApplyOverlayIntervals(resolved *ResolvedPricing) {
	if resolved == nil || resolved.Mode != BillingModeToken {
		return
	}
	// Channel interval pricing already populated Intervals and takes precedence.
	if len(resolved.Intervals) > 0 {
		return
	}
	if resolved.BasePricing == nil || len(resolved.BasePricing.Intervals) == 0 {
		return
	}
	resolved.Intervals = filterValidIntervals(resolved.BasePricing.Intervals)
}

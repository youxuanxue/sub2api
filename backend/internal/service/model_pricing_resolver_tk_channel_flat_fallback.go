package service

// model_pricing_resolver_tk_channel_flat_fallback.go — TokenKey channel
// pricing fallback policy.
//
// Upstream Wei-Shaw/sub2api#2107 root cause: when a channel configured BOTH
// token-interval pricing AND flat default fields (InputPrice / OutputPrice /
// Cache* / ImageOutputPrice), the resolver's applyTokenOverrides early-
// returned after attaching intervals and never wrote the operator's flat
// defaults to BasePricing. An out-of-range request (token count outside
// all configured intervals) then fell back to LiteLLM catalog pricing — or
// to nil for custom models with no LiteLLM entry, billing the request at $0.
//
// The TK policy: regardless of whether intervals are also configured, the
// channel's flat fields are written to BasePricing so GetIntervalPricing's
// out-of-range fallback returns operator-intended values.
//
// scripts/gateway-tk-sentinels.json pins both the call site in
// applyTokenOverrides (`tkApplyChannelFlatOverridesAsFallback`) and the
// helper definition here, so preflight + upstream-merge PR shape checks
// catch any revert.

// tkApplyChannelFlatOverridesAsFallback writes a channel's operator-configured
// flat pricing fields into resolved.BasePricing. When intervals are also
// configured these flat values act as the explicit out-of-range fallback for
// GetIntervalPricing. nil channel fields leave BasePricing entries untouched
// (the LiteLLM catalog base, if any, is preserved).
func tkApplyChannelFlatOverridesAsFallback(chPricing *ChannelModelPricing, resolved *ResolvedPricing) {
	if chPricing == nil || resolved == nil {
		return
	}
	if resolved.BasePricing == nil {
		resolved.BasePricing = &ModelPricing{}
	}

	if chPricing.InputPrice != nil {
		resolved.BasePricing.InputPricePerToken = *chPricing.InputPrice
		resolved.BasePricing.InputPricePerTokenPriority = *chPricing.InputPrice
	}
	if chPricing.OutputPrice != nil {
		resolved.BasePricing.OutputPricePerToken = *chPricing.OutputPrice
		resolved.BasePricing.OutputPricePerTokenPriority = *chPricing.OutputPrice
	}
	if chPricing.CacheWritePrice != nil {
		resolved.BasePricing.CacheCreationPricePerToken = *chPricing.CacheWritePrice
		resolved.BasePricing.CacheCreation5mPrice = *chPricing.CacheWritePrice
		resolved.BasePricing.CacheCreation1hPrice = *chPricing.CacheWritePrice
	}
	if chPricing.CacheReadPrice != nil {
		resolved.BasePricing.CacheReadPricePerToken = *chPricing.CacheReadPrice
		resolved.BasePricing.CacheReadPricePerTokenPriority = *chPricing.CacheReadPrice
	}
	if chPricing.ImageOutputPrice != nil {
		resolved.BasePricing.ImageOutputPricePerToken = *chPricing.ImageOutputPrice
	}
}

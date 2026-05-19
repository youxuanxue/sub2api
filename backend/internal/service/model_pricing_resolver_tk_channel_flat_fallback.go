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
// scripts/sentinels/gateway-tk.json pins both the call site in
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

// tkOverlayIntervalOntoBasePricing returns the effective pricing for a matched
// interval by overlaying the interval's non-nil fields onto a copy of
// resolved.BasePricing. This preserves channel flat defaults (input / output /
// cache_read / cache_write / image_output) for any dimension the interval
// itself does not override.
//
// Upstream Wei-Shaw/sub2api#2363: the original intervalToModelPricing built a
// brand-new empty ModelPricing{} from interval fields only, so an in-range
// request silently lost the channel's flat CacheReadPrice (the common operator
// pattern: tiered input/output by interval + a single flat cache_read price).
// On TokenKey this is the same failure mode the #2107 fix solved for the
// out-of-range path — sticky-routing maximizes cache_read_input_tokens
// specifically to bank that discount, and dropping it makes the headline
// cost-savings feature silently inert.
func tkOverlayIntervalOntoBasePricing(base *ModelPricing, iv *PricingInterval, supportsCacheBreakdown bool) *ModelPricing {
	out := ModelPricing{SupportsCacheBreakdown: supportsCacheBreakdown}
	if base != nil {
		out = *base
		out.SupportsCacheBreakdown = supportsCacheBreakdown
	}
	if iv == nil {
		return &out
	}
	if iv.InputPrice != nil {
		out.InputPricePerToken = *iv.InputPrice
		out.InputPricePerTokenPriority = *iv.InputPrice
	}
	if iv.OutputPrice != nil {
		out.OutputPricePerToken = *iv.OutputPrice
		out.OutputPricePerTokenPriority = *iv.OutputPrice
	}
	if iv.CacheWritePrice != nil {
		out.CacheCreationPricePerToken = *iv.CacheWritePrice
		out.CacheCreation5mPrice = *iv.CacheWritePrice
		out.CacheCreation1hPrice = *iv.CacheWritePrice
	}
	if iv.CacheReadPrice != nil {
		out.CacheReadPricePerToken = *iv.CacheReadPrice
		out.CacheReadPricePerTokenPriority = *iv.CacheReadPrice
	}
	return &out
}

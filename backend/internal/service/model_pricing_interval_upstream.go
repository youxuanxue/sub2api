package service

func intervalToModelPricing(iv *PricingInterval, supportsCacheBreakdown bool, channelPricing *ChannelModelPricing) *ModelPricing {
	pricing := &ModelPricing{SupportsCacheBreakdown: supportsCacheBreakdown}
	if iv.InputPrice != nil {
		pricing.InputPricePerToken = *iv.InputPrice
		pricing.InputPricePerTokenPriority = *iv.InputPrice
	}
	if iv.OutputPrice != nil {
		pricing.OutputPricePerToken = *iv.OutputPrice
		pricing.OutputPricePerTokenPriority = *iv.OutputPrice
	}
	if iv.CacheWritePrice != nil {
		pricing.CacheCreationPricePerToken = *iv.CacheWritePrice
		pricing.CacheCreationPricePerTokenPriority = *iv.CacheWritePrice
		pricing.CacheCreationPriceExplicit = true
		pricing.CacheCreation5mPrice = *iv.CacheWritePrice
		pricing.CacheCreation1hPrice = *iv.CacheWritePrice
	}
	if iv.CacheReadPrice != nil {
		pricing.CacheReadPricePerToken = *iv.CacheReadPrice
		pricing.CacheReadPricePerTokenPriority = *iv.CacheReadPrice
	}
	if channelPricing != nil {
		pricing.ImageOutputPriceExplicit = true
		if channelPricing.ImageOutputPrice != nil {
			pricing.ImageOutputPricePerToken = *channelPricing.ImageOutputPrice
		}
	}
	return pricing
}

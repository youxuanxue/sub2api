package service

const MinAttributedCacheWriteTokens = 1024

// ModelDistinguishesCacheReadAndWrite reports whether the resolved official
// price card has separate cache-read pricing and a cache-write premium.
func (s *BillingService) ModelDistinguishesCacheReadAndWrite(model string) bool {
	if s == nil {
		return false
	}
	pricing, err := s.GetModelPricing(model)
	return err == nil &&
		pricing != nil &&
		pricing.InputPricePerToken > 0 &&
		pricing.CacheReadPricePerToken > 0 &&
		pricing.CacheCreationPricePerToken > pricing.InputPricePerToken
}

func shouldAttributeUpstreamCacheWrite(account *Account) bool {
	if account == nil {
		return false
	}
	return account.Platform == PlatformAntigravity ||
		(account.Platform == PlatformOpenAI && account.IsOpenAIOAuthLike()) ||
		account.Platform == PlatformNewAPI
}

func applyUpstreamCacheWriteAttribution(
	billingService *BillingService,
	account *Account,
	model string,
	cacheReadInputTokens, cacheCreationInputTokens, inputTokens *int,
	claudeSemantics bool,
) {
	if billingService == nil || !shouldAttributeUpstreamCacheWrite(account) ||
		!billingService.ModelDistinguishesCacheReadAndWrite(model) ||
		cacheReadInputTokens == nil || cacheCreationInputTokens == nil || inputTokens == nil ||
		*cacheReadInputTokens <= 0 || *cacheCreationInputTokens != 0 {
		return
	}

	uncached := *inputTokens
	if !claudeSemantics {
		uncached -= *cacheReadInputTokens
	}
	if uncached < MinAttributedCacheWriteTokens {
		return
	}

	*cacheCreationInputTokens = uncached
	if claudeSemantics {
		*inputTokens = 0
	}
}

// ApplyOpenAIUpstreamCacheReadWriteAttribution attributes an otherwise
// unsplit cached prompt from an eligible upstream OpenAI-style response.
func ApplyOpenAIUpstreamCacheReadWriteAttribution(
	billingService *BillingService,
	account *Account,
	model string,
	usage *OpenAIUsage,
) {
	if usage == nil {
		return
	}
	applyUpstreamCacheWriteAttribution(
		billingService, account, model,
		&usage.CacheReadInputTokens,
		&usage.CacheCreationInputTokens,
		&usage.InputTokens,
		false,
	)
}

// ApplyClaudeUpstreamCacheReadWriteAttribution attributes an otherwise
// unsplit cached prompt from an eligible upstream Claude-style response.
func ApplyClaudeUpstreamCacheReadWriteAttribution(
	billingService *BillingService,
	account *Account,
	model string,
	usage *ClaudeUsage,
) {
	if usage == nil {
		return
	}
	applyUpstreamCacheWriteAttribution(
		billingService, account, model,
		&usage.CacheReadInputTokens,
		&usage.CacheCreationInputTokens,
		&usage.InputTokens,
		true,
	)
}

func (s *OpenAIGatewayService) ApplyUpstreamCacheReadWriteAttribution(account *Account, result *OpenAIForwardResult) {
	if s == nil || result == nil {
		return
	}
	model := result.BillingModel
	if model == "" {
		model = result.Model
	}
	ApplyOpenAIUpstreamCacheReadWriteAttribution(s.billingService, account, model, &result.Usage)
}

func (s *GatewayService) ApplyUpstreamCacheReadWriteAttribution(account *Account, model string, usage *ClaudeUsage) {
	if s == nil {
		return
	}
	ApplyClaudeUpstreamCacheReadWriteAttribution(s.billingService, account, model, usage)
}

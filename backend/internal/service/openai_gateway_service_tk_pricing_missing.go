package service

// TK: OpenAI-compat record-usage funnel → pricing-missing Feishu notifier hook.
// Companion to the one-line injection in OpenAIGatewayService.RecordUsage's
// "openai_usage.pricing_missing_record_zero_cost" branch (which is upstream-owned
// observability and stays untouched). Service is NOT refused — see
// pricing_missing_notifier_tk.go for the design rationale.

// SetPricingMissingNotifier wires the pricing-missing Feishu notifier
// post-construction so the upstream constructor signature stays unchanged.
// nil = feature disabled.
func (s *OpenAIGatewayService) SetPricingMissingNotifier(n PricingMissingNotifier) {
	if s == nil {
		return
	}
	s.tkPricingMissingNotifier = n
}

// notifyOpenAIPricingMissing builds a PricingMissingEvent from the record-usage
// context and feeds the notifier. nil-safe on every input.
func (s *OpenAIGatewayService) notifyOpenAIPricingMissing(
	input *OpenAIRecordUsageInput,
	result *OpenAIForwardResult,
	apiKey *APIKey,
	billingModels []string,
	tokens UsageTokens,
) {
	if s == nil || s.tkPricingMissingNotifier == nil {
		return
	}
	ev := PricingMissingEvent{
		BillingModel: firstUsageBillingModel(billingModels),
		Tokens:       totalUsageTokensForPricingMissing(tokens),
	}
	if input != nil {
		ev.RequestedModel = input.OriginalModel
	}
	if result != nil {
		ev.UpstreamModel = result.UpstreamModel
		if ev.RequestedModel == "" {
			ev.RequestedModel = result.Model
		}
	}
	if apiKey != nil {
		ev.APIKeyID = apiKey.ID
		if apiKey.Group != nil {
			ev.GroupID = apiKey.Group.ID
			ev.GroupName = apiKey.Group.Name
			ev.Platform = apiKey.Group.Platform
		}
	}
	s.tkPricingMissingNotifier.NotifyPricingMissing(ev)
}

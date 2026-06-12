package service

// TK: OpenAI-compat record-usage funnel → pricing-missing Feishu notifier hook.
// Companion to the one-line injection in OpenAIGatewayService.RecordUsage's
// "openai_usage.pricing_missing_record_zero_cost" branch (which is upstream-owned
// observability and stays untouched). Service is NOT refused — see
// pricing_missing_notifier_tk.go for the design rationale.

// SetPricingMissingNotifier wires the pricing-missing / served-zero-cost Feishu
// notifier post-construction so the upstream constructor signature stays
// unchanged. nil = feature disabled. The actual notify trigger lives in the
// result-side probe (tkNotifyServedZeroCost in
// openai_gateway_service_tk_served_zero_cost.go); the former error-side
// notifyOpenAIPricingMissing was consolidated into it.
func (s *OpenAIGatewayService) SetPricingMissingNotifier(n PricingMissingNotifier) {
	if s == nil {
		return
	}
	s.tkPricingMissingNotifier = n
}

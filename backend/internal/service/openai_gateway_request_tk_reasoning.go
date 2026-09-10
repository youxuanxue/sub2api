package service

// A real reasoning model's explicit opt-out is not the synthetic single-choice
// entry advertised for non-reasoning Codex models. Resolve aliases through the
// selected account and reuse the catalog's model capability owners. This keeps
// request semantics; it does not certify that a provider accepts the effort.
func tkPreserveExplicitNoneReasoning(account *Account, model string) bool {
	if account == nil {
		return false
	}
	model = account.GetMappedModel(model)
	if isOpenAICodexReasoningGPTModel(model) || isDeepSeekCodexModel(model) {
		return true
	}
	metadata, ok := account.GetUpstreamModelMetadata(model)
	return ok && metadata.Reasoning != nil && *metadata.Reasoning
}

package service

// openAICodexSupportsResolvedModel owns supply-scoped model retirements. The
// 2026-10-15 live Codex and OpenAI edge responses reject GPT-5.5 with "not
// supported when using Codex with a ChatGPT account"; API suppliers still
// serve it. See docs/approved/candidate-eligibility-ssot.md.
//
// Reuse the wire alias owner so dated/effort spellings and compatibility aliases
// cannot revive the retired model. This is not a global catalog removal.
func openAICodexSupportsResolvedModel(account *Account, resolvedModel string) bool {
	if account == nil || (!account.IsOpenAIOAuthLike() && !isOpenAIEdgeMirrorAccount(account)) {
		return true
	}
	return normalizeKnownOpenAICodexModel(resolvedModel) != "gpt-5.5"
}

// Match the forwarding owner's mapping/passthrough behavior, rather than
// rejecting the raw client alias before an explicit supported remap.
func openAICodexSupportsRequestedModel(account *Account, requestedModel string) bool {
	if account == nil || (!account.IsOpenAIOAuthLike() && !isOpenAIEdgeMirrorAccount(account)) {
		return true
	}
	resolved := resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, false)
	return openAICodexSupportsResolvedModel(account, resolved)
}

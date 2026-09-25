package service

// tkApplyToolSearchHistoricalThinkingPrefilter is the Forward-path thin
// wrapper over the shared thinking-contract prefilters (FilterThinking +
// ToolSearch/tool-storm historical strip). Kept as a companion call site so
// upstream-shaped gateway_forward.go stays a one-line hook.
func tkApplyToolSearchHistoricalThinkingPrefilter(
	reqModel string,
	getBody func() []byte,
	replaceBody func([]byte) error,
) error {
	return replaceBody(tkApplyAnthropicThinkingContractPrefilters(getBody(), reqModel))
}

package service

// tkApplyToolSearchHistoricalThinkingPrefilter drops incompatible signed
// thinking history that ToolSearch dynamic loading can perturb before the
// first upstream hop.
func tkApplyToolSearchHistoricalThinkingPrefilter(
	reqModel string,
	getBody func() []byte,
	replaceBody func([]byte) error,
) error {
	return replaceBody(TkPrefilterToolSearchHistoricalThinking(getBody(), reqModel))
}

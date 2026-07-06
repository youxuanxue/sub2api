package service

// tkIsOpenAIEdgeMirrorStub reports whether account is a prod OpenAI apikey stub
// that forwards to an internal edge gateway (credentials.base_url
// https://api-<edge>.tokenkey.dev). Downstream "No available accounts" on these
// stubs is an edge-pool capacity signal, not OpenAI account health.
func tkIsOpenAIEdgeMirrorStub(account *Account) bool {
	return account != nil && account.Platform == PlatformOpenAI && isEdgeMirrorStub(account, edgeIDPattern)
}

// tkSkipOpenAIDownstreamCapacityPenalty is true when an OpenAI edge-mirror stub
// received TokenKey's own downstream pool-exhaustion envelope. Fail over without
// handle429 cooldown / runtime block and feed the bounded saturation preference.
func tkSkipOpenAIDownstreamCapacityPenalty(account *Account, statusCode int, upstreamMsg string, responseBody []byte) bool {
	if !tkIsOpenAIEdgeMirrorStub(account) {
		return false
	}
	if tkSkipDownstreamNoAvailableAccountsPenalty(statusCode, upstreamMsg, responseBody) {
		return true
	}
	return tkSkipDownstreamFailoverExhaustedPenalty(statusCode, upstreamMsg, responseBody)
}

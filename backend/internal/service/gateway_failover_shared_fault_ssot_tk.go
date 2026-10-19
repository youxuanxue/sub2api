package service

// SharedFault catalog for "retrying another account cannot help".
//
// Textual contract: docs/approved/gateway-failover-policy-ssot.md §SharedFault.
// This file is the only code owner of the OpenAI-compat / NewAPI bridge
// SharedFault predicates. Adapters must call gatewayCompatSharedFault (or submit
// gatewayFailureSemanticSharedFault after using these helpers) instead of
// maintaining a second stop-retry phrase table.
//
// Composition reuses existing detectors so OpenAI native and NewAPI bridge
// cannot drift on the same provider envelopes.

// gatewayCompatSharedFault reports request-scoped evidence where rotating the
// credential pool cannot change the outcome for the same prompt/body.
func gatewayCompatSharedFault(message string, body []byte) bool {
	if isOpenAISafetySessionBlockFault(message, body) {
		return true
	}
	if isOpenAIContextWindowError(message, body) {
		return true
	}
	// Policy / safety / invalid_request markers — including providers that wrap
	// a client block in a retryable 5xx status.
	return isOpenAINonRetryableClientError(message, body)
}

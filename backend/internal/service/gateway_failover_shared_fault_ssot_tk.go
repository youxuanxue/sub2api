package service

// SharedFault catalog for "retrying another account cannot help".
//
// Textual contract: docs/approved/gateway-failover-policy-ssot.md §SharedFault.
// This file is the only code owner of the OpenAI / NewAPI bridge SharedFault
// predicates. Adapters must call these helpers (or submit
// gatewayFailureSemanticSharedFault after using them) instead of maintaining a
// second stop-retry phrase table.
//
// Composition reuses existing detectors so OpenAI native and NewAPI bridge
// cannot drift on the same provider envelopes for the core catalog.

// gatewayCompatSharedFault reports request-scoped evidence where rotating the
// credential pool cannot change the outcome for the same prompt/body.
//
// Core catalog only: safety / response_protection / context-window. Do NOT fold
// isOpenAINonRetryableClientError here — that matcher treats type/code
// "invalid_request*" as SharedFault, but OpenAI reuses that envelope for
// capacity/overloaded/transient shapes that must still failover.
func gatewayCompatSharedFault(message string, body []byte) bool {
	if isOpenAISafetySessionBlockFault(message, body) {
		return true
	}
	if isOpenAIResponseProtectionUnavailable(message, body) {
		return true
	}
	return isOpenAIContextWindowError(message, body)
}

// gatewayNewAPIBridgeSharedFault extends the core catalog with OpenAI-compat
// client/policy markers. NewAPI relays sometimes wrap those blocks as 5xx; the
// extra matcher stops pool rotation. OpenAI native must keep using
// gatewayCompatSharedFault only (see gatewayCompatSharedFault comment).
func gatewayNewAPIBridgeSharedFault(message string, body []byte) bool {
	if gatewayCompatSharedFault(message, body) {
		return true
	}
	return isOpenAINonRetryableClientError(message, body)
}

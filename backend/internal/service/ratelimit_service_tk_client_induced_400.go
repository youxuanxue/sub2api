package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// tkIsAnthropicClientInducedBadRequest reports whether an Anthropic 400 response
// is a client-induced invalid_request_error (malformed params, illegal
// cache_control block count, unsupported field, etc.) rather than an
// account-level problem.
//
// TK (upstream Wei-Shaw/sub2api#2608): Anthropic account penalties are
// threshold-based — handleAnthropicUpstreamErrorWithOptions advances a per-account
// error counter and, once the threshold trips, cools the account down
// (SetTempUnschedulable). The three account-level 400 conditions (organization
// disabled / credit balance exhausted / identity verification required) are
// branched explicitly before the catch-all, so every *other* Anthropic 400 is an
// invalid_request_error caused by the CALLER's request. Counting those toward the
// pause threshold lets any caller routed to a shared OAuth subscription account
// disable it for everyone by sending a handful of malformed requests — a 503
// amplifier that an external client can trigger at will. Such 400s must fail back
// to the client unchanged without touching account health.
//
// Anthropic's error envelope is {"type":"error","error":{"type":"invalid_request_error", ...}}.
func tkIsAnthropicClientInducedBadRequest(responseBody []byte) bool {
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "error.type").String()))
	return errType == "invalid_request_error"
}

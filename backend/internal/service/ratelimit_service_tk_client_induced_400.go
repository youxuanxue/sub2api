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
//
// TK (G1): request_too_large is treated identically. Anthropic returns it (HTTP
// 413, occasionally 400) when the request body exceeds the upstream's own cap —
// after the request has already passed TokenKey's local body-limit middleware
// (handler.request_body_limit fast-fails oversize bodies with a local 413 before
// they ever reach an account). A request_too_large that still reaches the upstream
// is therefore purely a CALLER-side problem (too large a prompt / payload), never
// an account-health problem; a client looping 300KB+ Claude Code sessions must not
// be able to cool a shared OAuth account by tripping the 3/3 ladder. The dedicated
// case 413 in HandleUpstreamError covers the 413 status code; matching the type
// here additionally covers the rarer request_too_large-as-400 shape that flows
// through the case 400 catch-all.
func tkIsAnthropicClientInducedBadRequest(responseBody []byte) bool {
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "error.type").String()))
	return errType == "invalid_request_error" || errType == "request_too_large"
}

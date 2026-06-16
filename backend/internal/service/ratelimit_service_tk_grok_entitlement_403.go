package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// xAI gates the OAuth API surface (api.x.ai/v1) to SuperGrok HEAVY. A standard
// subscriber, an expired entitlement, or a downgraded plan gets HTTP 403 at
// INFERENCE time with a body like:
//
//	You have either run out of available resources or do not have an active
//	Grok subscription.
//
// This is a POOL-WIDE entitlement gate — every grok account shares the same xAI
// Heavy allowlist — so it behaves exactly like the capability-scope 401 (GPT专线
// P0): failing over just poisons each grok account in turn and then hits the
// empty-pool 429, and the generic 403→502 mask would hide the real cause from the
// operator. So a grok entitlement-403 must:
//  1. NOT trigger account failover (shouldFailoverOpenAIUpstreamResponse=false),
//  2. NOT cool/disable the account (no failover ⇒ HandleUpstreamError is not
//     reached on the native chat/image path),
//  3. surface a clean, actionable client error (NOT a masked 502).
//
// Kept in a TK-only companion file so future upstream merges of
// ratelimit_service.go / openai_gateway_service.go do not collide on this branch.
// See plan §403 honesty, NousResearch/hermes-agent#26847, openclaw/openclaw#84504.

const (
	tkGrokEntitlement403Subscription = "grok subscription"
	tkGrokEntitlement403Resources    = "run out of available resources"
	tkGrokEntitlement403Heavy        = "supergrok heavy"
)

// tkIsGrokEntitlement403 reports whether a 403 upstream response is the xAI
// Heavy-only entitlement gate (matched on xAI-specific body markers, so it is
// account-agnostic and cannot trip on a generic 403 / WAF block).
func tkIsGrokEntitlement403(statusCode int, body []byte) bool {
	if statusCode != 403 {
		return false
	}
	hay := tkGrokEntitlement403Haystack(body)
	if hay == "" {
		return false
	}
	return strings.Contains(hay, tkGrokEntitlement403Subscription) ||
		strings.Contains(hay, tkGrokEntitlement403Resources) ||
		strings.Contains(hay, tkGrokEntitlement403Heavy)
}

// tkGrokEntitlement403Haystack assembles the lower-cased structured error text to
// scan (extracted message + the common error.message / detail envelope fields),
// mirroring tkCapabilityScope401Haystack so a marker inside an unrelated field
// cannot match.
func tkGrokEntitlement403Haystack(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	if msg := strings.TrimSpace(extractUpstreamErrorMessage(body)); msg != "" {
		parts = append(parts, msg)
	}
	if errMsg := strings.TrimSpace(gjson.GetBytes(body, "error.message").String()); errMsg != "" {
		parts = append(parts, errMsg)
	}
	if detail := strings.TrimSpace(gjson.GetBytes(body, "detail").String()); detail != "" {
		parts = append(parts, detail)
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// tkGrokEntitlement403ClientMessage is the client-facing message for a grok
// entitlement-403, actionable without leaking which account.
func tkGrokEntitlement403ClientMessage(upstreamMsg string) string {
	base := "The Grok (xAI) subscription backing this request is not entitled to API access " +
		"(xAI gates the OAuth API surface to SuperGrok Heavy). Confirm the account is on SuperGrok Heavy."
	upstreamMsg = strings.TrimSpace(upstreamMsg)
	if upstreamMsg == "" {
		return base
	}
	return base + " Upstream detail: " + upstreamMsg
}

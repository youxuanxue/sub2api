package service

import "net/http"

// tkSkipRelayedCanonicalIngressRejectPenalty reports whether an Anthropic
// upstream 403 is a downstream TokenKey edge's own canonical-ingress strict
// rejection (PR #691) rather than a health problem with THIS forwarding stub
// account.
//
// Topology: on the prod main gateway, Anthropic is reached only through
// per-edge api-key mirror "stub" accounts (cc-us1 / cc-uk1 / …). When an edge
// runs with anthropic_canonical_ingress_strict_enabled=true (single-edge canary
// for relaxing cc_only), every non-CC / empty-UA client request relayed through
// that stub comes back as a local 403 whose body carries TokenKey's own
// canonicalIngressRejectNeedle phrase. That verdict is about the END CLIENT's
// identity — the stub and the edge are perfectly healthy.
//
// Without this skip, three such rejections within a minute would advance the
// per-account anthropic_upstream_error 3/3 ladder (handle403 →
// handleAnthropicUpstreamError) and cool the canary edge's stub for up to 10
// minutes — i.e. any single third-party client probing prod could evict the
// canary edge from the pool and drag legitimate CC traffic with it (the same
// self-inflicted amplifier shape as the 2026-05-31 "no available accounts"
// incident). Instead: fail the in-flight request over to the next stub (a
// non-strict edge will serve it during canary) and leave stub health untouched.
//
// Boundary discipline (mirrors tkSkipDownstreamNoAvailableAccountsPenalty):
// match ONLY TokenKey's own rejection phrase. A genuine Anthropic 403 carries no
// such phrase and falls through to the rest of handle403: an account-fatal
// org-level ban ("not allowed for this organization" / "organization has been
// disabled") is permanently disabled by tkTryDisableAnthropicOrgBan403, a
// TLS/bot challenge takes the short TLS-fingerprint cooldown, and any other 403
// flows through the tiered upstream-error cooldown.
func tkSkipRelayedCanonicalIngressRejectPenalty(statusCode int, upstreamMsg string, responseBody []byte) bool {
	if statusCode != http.StatusForbidden {
		return false
	}
	return IsCanonicalIngressUARejectMessage(responseBody, upstreamMsg)
}

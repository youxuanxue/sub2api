package service

import "context"

// IsAnthropicCanonicalIngressStrictEnabled reports whether the canonical
// Anthropic OAuth ingress should enforce the strict allow-list UA gate (on both
// /v1/messages and count_tokens) — the "reject at the door" strategy. Defaults
// to false: without an explicit opt-in the canonical path keeps its deny-list /
// upstream behavior (zero regression). Scoped to anthropic OAuth accounts bound
// to the canonical TLS profile only.
//
// Orthogonal to IsAnthropicCanonicalHaikuMimicryEnabled — see
// SettingKeyAnthropicCanonicalIngressStrictEnabled for why the two toggles are
// split and must not be assumed to move together.
//
// Reads through the shared 60s gatewayForwardingCache (singleflight) — the same
// cache every adjacent hot-path Anthropic forwarding setting uses, refreshed in
// place by the settings pubsub on admin update (toggle without a deploy). A
// direct per-request settingRepo.GetValue here would add a DB roundtrip to
// every canonical /v1/messages + count_tokens request and silently fail-open on
// a DB blip; the cache serves the last-known value instead.
func (s *SettingService) IsAnthropicCanonicalIngressStrictEnabled(ctx context.Context) bool {
	if s == nil {
		return false
	}
	return s.getGatewayForwardingSettingsCached(ctx).canonicalIngressStrict
}

// IsAnthropicCanonicalHaikuMimicryEnabled reports whether a non-CC haiku request
// on a canonical Anthropic OAuth account should also receive the CC system/billing
// block injection — the "admit and launder" strategy that completes the egress
// mimicry so non-CC haiku does not leave the edge as a half-disguised cohort.
// Defaults to false (keep the haiku skip, zero regression). Scoped to anthropic
// OAuth + canonical TLS only.
//
// Orthogonal to IsAnthropicCanonicalIngressStrictEnabled: this switch never
// rejects a client, it only completes the outbound disguise. It is the toggle to
// pair with relaxing cc_only and routing non-CC traffic to a canonical fallback
// account. See SettingKeyAnthropicCanonicalHaikuMimicryEnabled.
//
// Reads through the same shared 60s gatewayForwardingCache as above.
func (s *SettingService) IsAnthropicCanonicalHaikuMimicryEnabled(ctx context.Context) bool {
	if s == nil {
		return false
	}
	return s.getGatewayForwardingSettingsCached(ctx).canonicalHaikuMimicry
}

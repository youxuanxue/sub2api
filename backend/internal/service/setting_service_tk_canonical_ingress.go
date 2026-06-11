package service

import "context"

// IsAnthropicCanonicalIngressStrictEnabled reports whether the canonical
// Anthropic OAuth ingress should enforce the strict allow-list UA gate, the
// haiku mimicry completion, and the count_tokens UA gate. It defaults to false:
// without an explicit opt-in the canonical path keeps its current deny-list /
// upstream behavior (zero regression). When enabled, the hardening described in
// SettingKeyAnthropicCanonicalIngressStrictEnabled takes effect, scoped to
// anthropic OAuth accounts bound to the canonical TLS profile only.
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

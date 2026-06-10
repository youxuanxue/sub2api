package service

import (
	"fmt"
	"net/http"
	"strings"
)

// TokenKey canonical-OAuth ingress gates. When an Anthropic OAuth account binds
// the canonical Claude Code TLS profile (tk_canonical_cc_oauth),
// only the real claude-cli / claude-sdk identity is allowed to route through it,
// and only models that the current Claude Code CLI release actually emits are
// accepted upstream. Both gates collapse the cohort signal that triggered the
// 2026-05-25 Anthropic hold on edge-uk1 account EN-LD-EC2-16-3:
//   1. third-party SDK UAs (OpenAI/Python, httpx, requests, ...) silently
//      draining a personal Claude Code subscription
//   2. retired models (claude-opus-4-6 / claude-opus-4-5*) routed alongside the
//      current 4-7 default — a mix-pattern real cc clients no longer produce.
//
// Both gates are scoped to OAuth + canonical TLS only; API-key channels and
// non-canonical OAuth accounts keep upstream-default behavior.

// CanonicalIngressUARejectedError signals that an ingress request was rejected
// because its User-Agent is not a real claude-cli / claude-sdk client on the
// canonical Anthropic OAuth path. Handler converts this to HTTP 403.
type CanonicalIngressUARejectedError struct {
	IngressUA string
}

func (e *CanonicalIngressUARejectedError) Error() string {
	return fmt.Sprintf("canonical claude oauth path rejects non-cc client: ingress user_agent=%q (use claude-cli)", e.IngressUA)
}

// canonicalIngressUAForbiddenSubstrings lists case-insensitive substrings that
// identify well-known third-party SDK / CLI clients. Anything matching is
// rejected; everything else (including unknown UAs and empty UAs) is allowed.
// Real Claude Code clients use "claude-cli/" or "claude-code/" prefixes and
// will never match these substrings.
//
// Append-only at runtime. Entries are deliberately precise (avoiding short
// generic tokens like "got/" or "requests/" that would false-positive on
// legitimate UAs ending in those substrings) — favor missing a rare attacker
// UA over rejecting a legitimate client. Generic third-party SDKs we cannot
// rule out cleanly stay off the list.
var canonicalIngressUAForbiddenSubstrings = []string{
	"openai/python",
	"openai-python",
	"httpx/",
	"python-requests/",
	"node-fetch",
	"axios/",
	"got (",
	"undici",
	"go-http-client",
	"curl/",
	"wget/",
	"postman",
	"insomnia",
	"libcurl",
	"okhttp",
	"java/",
	"reqwest/",
	"aiohttp",
}

// checkCanonicalIngressUA validates ingress User-Agent on the canonical OAuth
// path. Returns nil to allow forwarding; returns *CanonicalIngressUARejectedError
// to block with HTTP 403.
//
// Policy: deny-list-only. An empty UA is allowed (prod→edge relay may strip it
// and we already pin canonical UA upstream). An unknown UA is allowed so that a
// future Claude Code variant (e.g. claude-cli/2.2 IDE build) does not need a
// code change to keep working. Only explicit third-party SDK substrings reject.
func checkCanonicalIngressUA(headers http.Header) error {
	ua := strings.TrimSpace(headers.Get("User-Agent"))
	if ua == "" {
		return nil
	}
	lower := strings.ToLower(ua)
	for _, s := range canonicalIngressUAForbiddenSubstrings {
		if strings.Contains(lower, s) {
			return &CanonicalIngressUARejectedError{IngressUA: ua}
		}
	}
	return nil
}

// canonicalIngressUAAllowedPrefixes lists the case-insensitive UA prefixes that
// the strict allow-list gate accepts. Only real Claude Code clients emit these
// ("claude-cli/" is the CLI/SDK, "claude-code/" is the IDE/extension build);
// everything else — including empty and unknown UAs — is rejected.
var canonicalIngressUAAllowedPrefixes = []string{
	"claude-cli/",
	"claude-code/",
}

// checkCanonicalIngressUAStrict is the allow-list counterpart of
// checkCanonicalIngressUA, used only when
// SettingKeyAnthropicCanonicalIngressStrictEnabled is true. It returns nil only
// when the trimmed, lower-cased User-Agent starts with one of
// canonicalIngressUAAllowedPrefixes; an empty UA or any unknown UA returns
// *CanonicalIngressUARejectedError (HTTP 403 at the handler).
//
// Rationale: with cc_only relaxed on a group, the canonical OAuth path is the
// only client-identity gate left for a personal subscription's edge account. An
// empty UA is a real passed-through client signal on the transparent prod→edge
// relay (not a relay artifact), so strict mode must require an explicit CC
// identity rather than tolerating anonymous traffic. The default deny-list path
// (checkCanonicalIngressUA) is preserved unchanged for zero regression.
func checkCanonicalIngressUAStrict(headers http.Header) error {
	ua := strings.TrimSpace(headers.Get("User-Agent"))
	lower := strings.ToLower(ua)
	for _, p := range canonicalIngressUAAllowedPrefixes {
		if strings.HasPrefix(lower, p) {
			return nil
		}
	}
	return &CanonicalIngressUARejectedError{IngressUA: ua}
}

// shouldRewriteSystemForNonCCMimicry decides whether the OAuth mimicry path
// should rewrite the system prompt for a non-CC request. Default behavior skips
// haiku (it carries CC headers but no CC system rewrite). When canonicalStrict
// is true the haiku skip is lifted so the cohort stays consistent (CC headers +
// CC system/billing block together). Non-haiku models are always rewritten.
// Pure predicate so the gating contract is unit-testable in isolation.
func shouldRewriteSystemForNonCCMimicry(reqModel string, canonicalStrict bool) bool {
	if canonicalStrict {
		return true
	}
	return !strings.Contains(strings.ToLower(reqModel), "haiku")
}

// canonicalDefaultOpus is the current Claude Code default opus tier; deprecated
// opus model ids are remapped to this on the canonical OAuth path.
const canonicalDefaultOpus = "claude-opus-4-7"

// canonicalDeprecatedOpusPrefixes lists the opus model ids that the 2.1.150
// Claude Code CLI no longer emits by default; routing them alongside 4-7
// produces a mix-pattern that current real clients do not. Each entry is the
// model-id prefix (so dated variants like claude-opus-4-6-20250930 are caught).
var canonicalDeprecatedOpusPrefixes = []string{
	"claude-opus-4-6",
	"claude-opus-4-5",
	"claude-opus-4-4",
	"claude-opus-4-3",
	"claude-opus-4-2",
	"claude-opus-4-1",
	"claude-opus-4-0",
}

// remapDeprecatedOpusOnCanonical returns the canonical opus model id if the
// input matches a known retired prefix, plus a remapped=true flag. Non-opus
// models and the current default pass through unchanged.
func remapDeprecatedOpusOnCanonical(model string) (string, bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return model, false
	}
	lower := strings.ToLower(trimmed)
	for _, p := range canonicalDeprecatedOpusPrefixes {
		if lower == p || strings.HasPrefix(lower, p+"-") {
			return canonicalDefaultOpus, true
		}
	}
	return model, false
}

// isCanonicalAnthropicOAuth reports whether the account is an Anthropic OAuth
// account bound to the canonical TLS fingerprint profile. Callers use this to
// gate the ingress UA / deprecated-model policies above.
func (s *GatewayService) isCanonicalAnthropicOAuth(account *Account) bool {
	if account == nil || account.Platform != PlatformAnthropic || !account.IsOAuth() {
		return false
	}
	return IsCanonicalTLSProfileName(s.tlsFingerprintProfileNameForAccount(account))
}

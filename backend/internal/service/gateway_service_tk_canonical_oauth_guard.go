package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// CanonicalIngressUARejectedError signals that an ingress request was rejected
// because its User-Agent is not a real claude-cli / claude-sdk client on the
// canonical Anthropic OAuth path. Handler converts this to HTTP 403.
type CanonicalIngressUARejectedError struct {
	IngressUA string
}

const canonicalIngressRejectNeedle = "canonical claude oauth path rejects non-cc client"

func (e *CanonicalIngressUARejectedError) Error() string {
	return fmt.Sprintf("%s: ingress user_agent=%q (use claude-cli)", canonicalIngressRejectNeedle, e.IngressUA)
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

// canonicalDeprecatedOpusPrefixes lists opus model ids to remap to the current
// default. Each entry is a model-id prefix (so dated variants like
// claude-opus-4-5-20250520 are caught too).
//
// NOTE: claude-opus-4-6 is intentionally NOT in this list. It is still served
// upstream and — unlike opus >= 4-7 — still returns PLAINTEXT extended thinking
// (verified 2026-06-13: opus-4-6 -> HTTP 200 with ~1800-3800 chars of thinking,
// while opus-4-7/4-8 return an empty thinking block + signature only). Remapping
// 4-6 -> 4-7 silently destroyed that thinking (and silently substituted the
// model the caller asked for / its billing key). The remap's original rationale
// ("retired opus no longer emits thinking") proved false for 4-6, so it passes
// through. opus-4-5 stays remapped only because it rejects the thinking shape we
// send (`adaptive`); 4-4 and older are genuinely 404 upstream.
var canonicalDeprecatedOpusPrefixes = []string{
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

// tkGroupAdmitsNonCC reports whether the request's effective group has opted into
// serving non-Claude-Code traffic (group.ClaudeCodeOnly == false). This is the
// single operator-facing control that, on a canonical OAuth account, switches the
// non-CC policy from "reject at ingress" (deny-list 403) to "admit and launder on
// the wire" (the existing egress mimicry path completes the CC disguise).
//
// Rationale (no separate setting): cc_only IS the switch. A non-CC client only
// ever reaches forwarding under a cc_only=false group — the cc-only gate
// (gateway_service.go, ErrClaudeCodeOnly) follows fallbacks until the resolved
// group is non-cc-only or the client is real CC. So when the deny-list would fire
// (always on a non-CC UA), the operator has already declared admission via
// cc_only=false; re-rejecting at the account level only contradicts that intent.
//
// Fail-closed: a nil request, missing group id, or resolution error returns false
// so the canonical deny-list guard stays in force (zero regression on the unknown
// path). The explicit strict-ingress toggle is unaffected — it remains the
// override for operators who want to reject all non-CC even under cc_only=false.
func (s *GatewayService) tkGroupAdmitsNonCC(ctx context.Context, parsed *ParsedRequest) bool {
	if parsed == nil || parsed.GroupID == nil {
		return false
	}
	g, err := s.resolveGroupByID(ctx, *parsed.GroupID)
	if err != nil || g == nil {
		return false
	}
	return !g.ClaudeCodeOnly
}

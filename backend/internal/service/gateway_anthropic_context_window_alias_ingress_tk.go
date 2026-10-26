package service

import (
	"github.com/tidwall/sjson"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TokenKey: apply the context-window alias strip at the HANDLER throat, before
// account selection — not only on the forward hop.
//
// Why the forward-stage strip is not enough
// -----------------------------------------
// tkStripContextWindowModelAlias (gateway_anthropic_context_window_alias_tk.go,
// #537) repairs the `model` field so Anthropic never sees "claude-opus-4-8[1m]".
// Its three call sites all live AFTER an account has been picked:
// tkPrepareAnthropicForwardIngress, the API-Key passthrough prep and
// count_tokens prep. Account selection runs strictly earlier —
// handler.SelectAccountWithLoadAwareness(..., reqModel, ...) — so the per-account
// model-support predicate saw the BRACKETED id and answered "unsupported":
//
//	| requested                 | empty-mapping Anthropic | Kiro  | explicit model_mapping |
//	| claude-sonnet-5           | true                    | true  | true                   |
//	| claude-sonnet-5[1m]       | true                    | FALSE | FALSE                  |
//
// Empty-mapping Anthropic accounts escaped only because Account.IsModelSupported
// short-circuits "no mapping = allow all" — which is why this never showed up as
// a fleet-wide outage. Kiro accounts (kiroSupportsModel, an exact catalog
// lookup) and any account with an explicit model_mapping were excluded from the
// candidate set, and the request died before reaching the code that would have
// stripped the suffix.
//
// Downstream amplifiers, both closed by stripping here:
//
//   - tkGroupUnsupportedModelCacheKey keys the 60s negative cache through
//     CanonicalizeOpenAICompatRoutingModel, which keeps a trailing "[1m]" (it is
//     not a Codex id). One selection failure therefore poisoned group×model for
//     a full minute of instant failures.
//   - tkWrapSelectionFailure classifies all-model_unsupported stats as 400
//     "Unsupported model: claude-sonnet-5[1m]" and mixed stats as 429. Claude
//     Code renders both as "temporarily unavailable" and drops auto mode back to
//     manual confirmation.
//
// Relationship to the existing call sites
// ---------------------------------------
// The forward / passthrough / count_tokens strips STAY (project rule §5.x:
// default is keep, never silent-delete). They cover ingress points outside this
// handler throat, and the transform is idempotent, so re-stripping an id this
// function already made bare is a no-op.
//
// The 1M window semantics are unchanged: Claude Code activates it through the
// separate `anthropic-beta: context-1m-2025-08-07` header, which flows through
// evaluateBetaPolicy untouched. Only the `model` field is repaired.

// tkContextWindowAliasIngressPlatform gates the ingress strip to the
// Anthropic-shaped native-Claude platforms:
//
//   - "" — universal key / no forced platform / composite, i.e. the platform is
//     not yet decided and the account that wins may be any of the below.
//   - anthropic — native relays, OAuth and API-Key, plus Kiro and Antigravity
//     accounts mixed-scheduled into an Anthropic group (they report the GROUP
//     platform here).
//   - kiro — a direct Kiro-platform group, where the same exact-catalog lookup
//     rejects the bracketed id.
//
// Gemini, Antigravity-forced and every OpenAI-compat platform are intentionally
// out of scope: the OpenAI-compat request shape already strips the same suffix
// at its own normalization seam (openai_compat_context_window_alias_tk.go).
func tkContextWindowAliasIngressPlatform(platform string) bool {
	switch platform {
	case "", PlatformAnthropic, PlatformKiro:
		return true
	default:
		return false
	}
}

// TkStripContextWindowAliasAtIngress removes a trailing Claude Code
// context-window alias from parsed.Model and from the wire body, at the handler
// throat — before channel mapping, session hash, scheduling and the negative
// cache key are derived.
//
// It must run BEFORE TkApplyBareModelAlias so bare-family resolution sees a
// clean id. (tkResolveBareModelAlias strips internally for its own lookup, which
// is why "sonnet[1m]" already resolved; a FULL id such as "claude-sonnet-5[1m]"
// is an alias-table miss and is exactly the class that leaked through.)
//
// On a hit it rewrites only the body's model field (sjson), refreshes parsed
// (body bytes + Model) and returns the new body plus the bare id. On a miss — or
// any rewrite failure — it returns (nil, "") with parsed untouched, so the
// caller's body stays byte-identical.
func TkStripContextWindowAliasAtIngress(platform string, parsed *ParsedRequest) ([]byte, string) {
	if parsed == nil || parsed.Body == nil || parsed.Body.Len() == 0 {
		return nil, ""
	}
	if !tkContextWindowAliasIngressPlatform(platform) {
		return nil, ""
	}
	requested := parsed.Model
	bare, stripped := tkStripContextWindowModelAlias(requested)
	if !stripped {
		return nil, ""
	}
	newBody, err := sjson.SetBytes(parsed.Body.Bytes(), "model", bare)
	if err != nil {
		return nil, ""
	}
	if err := parsed.ReplaceBody(newBody); err != nil {
		return nil, ""
	}
	parsed.Model = bare
	logger.LegacyPrintf("service.gateway",
		"TK context-window alias stripped at ingress before account selection (claude-code #60913): requested=%s bare=%s platform=%s",
		requested, bare, platform)
	return newBody, bare
}

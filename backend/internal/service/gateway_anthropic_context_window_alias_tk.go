package service

import "regexp"

// TokenKey: strip the Claude Code context-window model alias suffix (e.g.
// "claude-opus-4-8[1m]") from an outbound Anthropic model id before forward.
//
// Why this lives in TokenKey (gateway) and not in the client
// -----------------------------------------------------------
// Claude Code denotes the 1M-context variant of a model with an INTERNAL alias
// suffix — "claude-opus-4-8[1m]", "claude-opus-4-7[1m]". On session resume /
// continue / a `/compact`-triggered SessionStart, the CLI serializes that
// bracketed alias VERBATIM into the API `model` field. The Anthropic API does
// not recognize it and answers with a hard 404:
//
//	{"type":"error","error":{"type":"not_found_error",
//	 "message":"model: claude-opus-4-7[1m]"}}
//
// after which the client SILENTLY falls back to the bare 200K model — the user
// keeps paying for / expecting 1M context but never gets it, with no error
// surfaced. Tracked upstream as anthropics/claude-code#60913 (24h proxy
// capture: 9 requests with the `[1m]` alias → all 404, 5697 subsequent bare
// requests → all 200, and `/context` then reports `/200k`). The same fragile
// alias-handling code surfaces in #50803 (`--model` drops `[1m]`), #53031,
// #55504 and #34143.
//
// A relay is the single choke point that sees every request and can normalize
// the model field before it reaches Anthropic — the same posture TokenKey
// already takes for thinking.type=adaptive (#514), empty text blocks and UTF-8
// surrogates (#60168/#63885/#64777). We repair PRE-FLIGHT rather than on a 404
// retry: a bracketed alias is NEVER a legitimate Anthropic model id, so there
// is no valid body to misclassify, and repairing it up front avoids the
// otherwise-guaranteed failed round trip plus the silent 200K downgrade.
//
// The 1M window is preserved without any beta-header work: Claude Code already
// sends `context-1m-2025-08-07` in the `anthropic-beta` header separately, and
// that header flows through evaluateBetaPolicy unchanged — so bare model id +
// the existing beta token still activates the 1M context window. The bug is
// isolated to the `model` field; stripping the alias is sufficient and complete.
//
// Safety contract
// ---------------
//   - Only a TRAILING bracketed context-window alias is removed; the base model
//     id is returned untouched.
//   - Real Anthropic model ids never contain `[`...`]`, so a bare id is returned
//     byte-for-byte unchanged with stripped=false — a pure no-op on 99.99% of
//     traffic and on every client version that already serializes correctly.
//   - The transform is idempotent: stripping an already-bare id is a no-op.
//
// This file is a `*_tk_*.go` companion to the upstream-owned gateway_service.go
// (TokenKey rule §5: keep fork-only request-shape logic out of the upstream file
// so `git merge upstream/main` stays conflict-free). The call sites in
// gateway_service.go are single additive hooks scoped to the Anthropic platform.

// tkContextWindowAliasSuffix matches a trailing Claude Code context-window alias
// such as "[1m]", "[1M]" or "[200k]" — an opening bracket, one or more digits, a
// unit letter (m/M/k/K) and a closing bracket, anchored to end-of-string. The
// anchor is what keeps the match conservative: a legitimate id is never touched.
var tkContextWindowAliasSuffix = regexp.MustCompile(`\[[0-9]+[mMkK]\]$`)

// tkStripContextWindowModelAlias returns model with any trailing context-window
// alias suffix removed. The second return value reports whether a suffix was
// stripped; when false the first return value is the input unchanged.
//
// This is the pure, side-effect-free core (exhaustively unit-tested in
// gateway_anthropic_context_window_alias_tk_test.go). Forward, the API-Key
// passthrough and count_tokens paths call it at their earliest model-resolution
// point and rewrite the wire body via s.replaceModelInBody, so every downstream
// consumer (model mapping, scheduling, pricing, sticky session) sees the bare id.
func tkStripContextWindowModelAlias(model string) (string, bool) {
	if model == "" {
		return model, false
	}
	loc := tkContextWindowAliasSuffix.FindStringIndex(model)
	if loc == nil {
		return model, false
	}
	return model[:loc[0]], true
}

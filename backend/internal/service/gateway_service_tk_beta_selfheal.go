package service

import (
	"context"
	"regexp"
	"strings"
)

// Anthropic-beta self-healing degradation.
//
// Anthropic periodically retires or renames anthropic-beta tokens. When TokenKey
// mimics Claude Code it pins a full beta set (claude.FullClaudeCodeMimicryBetas
// + manifest). The moment upstream drops one of those tokens, EVERY mimicked
// request to that account fails with a hard 400:
//
//	{"type":"error","error":{"type":"invalid_request_error",
//	 "message":"Unexpected value(s) `effort-2025-11-24` for the `anthropic-beta`
//	            header. Please consult our documentation ..."}}
//
// This exact regression hit real Claude Code itself (claude-code#53855, the
// effort-2025-11-24 token on a CLI version bump). Without a self-heal, a single
// upstream beta retirement turns into a full-account outage until an operator
// reships the manifest. The fix: detect the beta-rejection 400, parse out the
// offending token(s), drop them for ONE retry on the same account, and surface a
// loud ops signal so the manifest can be corrected out-of-band.
//
// The retry must go back through buildUpstreamRequest (so metadata rewrite + CCH
// signing + beta merge are redone correctly); a naive request clone would resend
// the unsigned body or re-add the rejected token. We thread the drop set through
// the request context (zero signature change) and buildUpstreamRequest merges it
// into the existing variadic mergeDropSets extra-tokens slot.

// anthropicBetaRejectionRe matches Anthropic's invalid_request_error for an
// unrecognized anthropic-beta value. Anchored on both the "unexpected value"
// phrasing and the "anthropic-beta" header name to avoid matching unrelated
// 400s (e.g. a body field literally named anthropic-beta).
var anthropicBetaRejectionRe = regexp.MustCompile(`(?is)unexpected value.*?anthropic-beta`)

// backtickTokenRe extracts the `token` values Anthropic quotes in backticks
// inside the rejection message, e.g. "Unexpected value(s) `effort-2025-11-24`".
var backtickTokenRe = regexp.MustCompile("`([A-Za-z0-9][A-Za-z0-9._:-]*)`")

// betaSelfHealDropCtxKey carries the rejected beta tokens to drop on a self-heal
// retry. Scoped to the retry request context only.
type betaSelfHealDropCtxKey struct{}

// withBetaSelfHealDrop returns a child context instructing buildUpstreamRequest
// to drop the given beta tokens from the final anthropic-beta header.
func withBetaSelfHealDrop(ctx context.Context, tokens []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(tokens) == 0 {
		return ctx
	}
	return context.WithValue(ctx, betaSelfHealDropCtxKey{}, tokens)
}

// betaSelfHealDropTokens reads the self-heal drop tokens injected for this
// request, or nil. Fed into mergeDropSets(...)'s variadic extra slot.
func betaSelfHealDropTokens(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(betaSelfHealDropCtxKey{}).([]string); ok {
		return v
	}
	return nil
}

// parseRejectedAnthropicBetas returns the offending beta token(s) named in an
// Anthropic anthropic-beta rejection 400, or nil when the body is not a
// beta-rejection error. The returned tokens are exactly the values to drop.
func parseRejectedAnthropicBetas(respBody []byte) []string {
	if len(respBody) == 0 || !anthropicBetaRejectionRe.Match(respBody) {
		return nil
	}
	// Only scan the unexpected-value clause so we don't harvest unrelated
	// backtick-quoted tokens (e.g. a "try `anthropic-version`" hint).
	msg := string(respBody)
	if loc := anthropicBetaRejectionRe.FindStringIndex(msg); loc != nil {
		// Extend a little past the match to capture the quoted value list that
		// typically precedes "for the `anthropic-beta` header".
		start := loc[0]
		end := loc[1] + 160
		if end > len(msg) {
			end = len(msg)
		}
		msg = msg[start:end]
	}
	seen := make(map[string]struct{})
	var out []string
	for _, m := range backtickTokenRe.FindAllStringSubmatch(msg, -1) {
		tok := strings.TrimSpace(m[1])
		// "anthropic-beta" is the header name Anthropic quotes, not a value to
		// drop; skip it and any empty capture.
		if tok == "" || strings.EqualFold(tok, "anthropic-beta") {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out
}

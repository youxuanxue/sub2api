package service

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// TokenKey: friendly 400 gate for retired / soon-to-sunset Anthropic model IDs.
//
// Why this exists: PR #329 / #327 removed retired Claude model IDs from the
// admin dropdowns + default fallback chain, but TokenKey itself did not
// short-circuit requests that still used those IDs. Without a gate, hardcoded
// clients hit one of two failure modes:
//   - account has no model_mapping → request passes through to Anthropic and
//     fails with the upstream `not_found_error` (Claude 4.0 family until the
//     2026-06-15 sunset, others already today).
//   - account has model_mapping → admin's explicit mapping wins; deprecation
//     check below runs on the *mapped* model so this case is naturally
//     ignored (operator opt-in).
//
// The retired list intentionally includes the Claude 4.0 snapshots that
// Anthropic accepts until 2026-06-15: surfacing the deprecation 25 days early
// is the explicit product choice (`/xj-review` R-001 resolution, decision
// path (c)) — a clear migration signal beats a silent upstream 404 cliff.
//
// Scope: native /v1/messages forward path + count_tokens path. Passthrough
// (IsAnthropicAPIKeyPassthroughEnabled) and Bedrock are intentionally NOT
// covered — passthrough is a "raw bytes" promise we do not modify, and
// Bedrock keeps its own model lifecycle independent of Anthropic main
// (`backend/internal/domain/constants.go` DefaultBedrockModelMapping).
//
// The check fires on the *mapped* model (after account.GetMappedModel /
// claude.NormalizeModelID), so admin-configured rewrites such as
// `claude-3-5-sonnet-20241022 -> claude-sonnet-4-6` continue to work — the
// rewritten target is not in the retired list.

const (
	// tkDeprecatedAnthropicReplacementSonnet — recommended Sonnet replacement.
	tkDeprecatedAnthropicReplacementSonnet = "claude-sonnet-4-6"
	// tkDeprecatedAnthropicReplacementOpus — recommended Opus replacement.
	tkDeprecatedAnthropicReplacementOpus = "claude-opus-4-7"
	// tkDeprecatedAnthropicErrorType — Anthropic error envelope's inner type.
	tkDeprecatedAnthropicErrorType = "invalid_request_error"
)

// tkDeprecatedAnthropicModels maps every retired or soon-to-sunset Anthropic
// model snapshot ID to its recommended TokenKey replacement. The map is the
// single source of truth for both the deprecation gate below and the
// matching unit test. Adding a new entry here is sufficient; no other call
// site needs to be updated.
//
// Sunset references (verified 2026-05-21 against
// https://docs.anthropic.com/en/docs/about-claude/models/all-models#legacy-models):
//   - claude-3-haiku-20240307, claude-3-sonnet-20240229, claude-3-opus-20240229:
//     legacy family, already retired.
//   - claude-3-5-haiku-20241022, claude-3-5-sonnet-20240620,
//     claude-3-5-sonnet-20241022, claude-3-7-sonnet-20250219: 3.5 / 3.7 family,
//     retired by PR #327 (`a9a67000`).
//   - claude-sonnet-4-20250514, claude-opus-4-20250514: 4.0 family, Anthropic
//     sunset 2026-06-15, retired by PR #329 (`0c2bdfb2`).
var tkDeprecatedAnthropicModels = map[string]string{
	"claude-3-haiku-20240307":    tkDeprecatedAnthropicReplacementSonnet,
	"claude-3-sonnet-20240229":   tkDeprecatedAnthropicReplacementSonnet,
	"claude-3-opus-20240229":     tkDeprecatedAnthropicReplacementOpus,
	"claude-3-5-haiku-20241022":  tkDeprecatedAnthropicReplacementSonnet,
	"claude-3-5-sonnet-20240620": tkDeprecatedAnthropicReplacementSonnet,
	"claude-3-5-sonnet-20241022": tkDeprecatedAnthropicReplacementSonnet,
	"claude-3-7-sonnet-20250219": tkDeprecatedAnthropicReplacementSonnet,
	"claude-sonnet-4-20250514":   tkDeprecatedAnthropicReplacementSonnet,
	"claude-opus-4-20250514":     tkDeprecatedAnthropicReplacementOpus,
}

// tkIsDeprecatedAnthropicModel reports whether the (already mapped) model ID
// is on the retired list, and returns the recommended replacement. Lookups
// are exact-match: TokenKey upstream accepts both short and long IDs, and
// Claude OAuth paths run claude.NormalizeModelID before this hook, so the
// long-ID exact match catches both call styles.
func tkIsDeprecatedAnthropicModel(model string) (string, bool) {
	if model == "" {
		return "", false
	}
	replacement, ok := tkDeprecatedAnthropicModels[model]
	return replacement, ok
}

// tkWriteAnthropicDeprecatedModelError emits the Anthropic-shape 400 error
// for a retired model request and aborts further gin handling. Returning
// `true` lets callers `return` immediately without writing their own
// response. Safe to call with c == nil (returns false, no-op).
func tkWriteAnthropicDeprecatedModelError(c *gin.Context, requestedModel, replacement string) bool {
	if c == nil {
		return false
	}
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    tkDeprecatedAnthropicErrorType,
			"message": tkBuildDeprecatedAnthropicMessage(requestedModel, replacement),
		},
	})
	return true
}

// tkBuildDeprecatedAnthropicMessage assembles the friendly migration message.
// Kept as a separate function so the unit test can assert message content
// without parsing JSON.
func tkBuildDeprecatedAnthropicMessage(requestedModel, replacement string) string {
	if replacement == "" {
		replacement = tkDeprecatedAnthropicReplacementSonnet
	}
	return "Model '" + requestedModel + "' is retired or scheduled for sunset by Anthropic" +
		" and has been removed from this TokenKey deployment. Please migrate to '" +
		replacement + "' (or '" + tkDeprecatedAnthropicReplacementOpus +
		"' for the Opus tier). See https://docs.anthropic.com/en/docs/about-claude/models/all-models" +
		" for the current model list."
}

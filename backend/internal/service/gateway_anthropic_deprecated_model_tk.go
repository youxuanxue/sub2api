package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
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
// Scope: native /v1/messages forward path + count_tokens path + account-selection
// failure (before Forward when no schedulable account serves the retired id).
// Passthrough (IsAnthropicAPIKeyPassthroughEnabled) and Bedrock are intentionally NOT
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
	// TkDeprecatedAnthropicErrorType — Anthropic error envelope's inner type.
	TkDeprecatedAnthropicErrorType = "invalid_request_error"
)

// ErrDeprecatedAnthropicModel reports that account selection failed while the
// requested model ID is on TokenKey's retired Anthropic list. This is a client
// error (HTTP 400), not a routing-capacity 429: prod saw clients hammering
// sunset snapshots get empty-pool 429 when every whitelist account rejected the
// name and at least one other candidate was unschedulable — the Forward-path
// deprecated gate never ran because no account was selected.
//
// Deliberately omits "no available accounts" so handler.isOpsNoAvailableAccountError
// does not relabel it as routing-capacity.
var ErrDeprecatedAnthropicModel = errors.New("deprecated anthropic model")

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

// TkLookupDeprecatedAnthropicModel resolves a client model name against the
// retired table, trying the raw id and claude.NormalizeModelID (same order as
// OAuth selection). Returns the matched table key for messaging.
func TkLookupDeprecatedAnthropicModel(requestedModel string) (matchedModel, replacement string, ok bool) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return "", "", false
	}
	seen := map[string]struct{}{requestedModel: {}}
	if replacement, ok := tkIsDeprecatedAnthropicModel(requestedModel); ok {
		return requestedModel, replacement, true
	}
	if normalized := claude.NormalizeModelID(requestedModel); normalized != "" {
		if _, dup := seen[normalized]; !dup {
			if replacement, ok := tkIsDeprecatedAnthropicModel(normalized); ok {
				return normalized, replacement, true
			}
		}
	}
	return "", "", false
}

// tkDeprecatedAnthropicSelectionFailure wraps ErrDeprecatedAnthropicModel when
// the requested model is retired. Used at account-selection failure so routing
// never surfaces a retry-storm 429 for a name that can never succeed as sent.
func tkDeprecatedAnthropicSelectionFailure(requestedModel string) error {
	if matched, replacement, ok := TkLookupDeprecatedAnthropicModel(requestedModel); ok {
		return fmt.Errorf("%w: %s (suggest %q)", ErrDeprecatedAnthropicModel, matched, replacement)
	}
	return nil
}

// TkSelectionNoAvailableAccountsError returns ErrDeprecatedAnthropicModel when
// requestedModel is on the retired list; otherwise ErrNoAvailableAccounts.
// Load-batch scheduling uses this instead of bare ErrNoAvailableAccounts so sunset
// ids classify as client 400, not routing 429 (see PR #1255 + load-batch gap).
func TkSelectionNoAvailableAccountsError(requestedModel string) error {
	if err := tkDeprecatedAnthropicSelectionFailure(requestedModel); err != nil {
		return err
	}
	return ErrNoAvailableAccounts
}

// TkWriteAnthropicDeprecatedModelError emits the Anthropic-shape 400 error
// for a retired model request and aborts further gin handling. Safe to call
// with c == nil (no-op).
func TkWriteAnthropicDeprecatedModelError(c *gin.Context, requestedModel, replacement string) {
	if c == nil {
		return
	}
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    TkDeprecatedAnthropicErrorType,
			"message": TkBuildDeprecatedAnthropicMessage(requestedModel, replacement),
		},
	})
}

// TkBuildDeprecatedAnthropicMessage assembles the friendly migration message.
// Kept as a separate function so the unit test can assert message content
// without parsing JSON. Callers must pass a non-empty replacement — the
// retired-model table guarantees this (every value is a Sonnet/Opus
// constant), so there is no defensive fallback here.
func TkBuildDeprecatedAnthropicMessage(requestedModel, replacement string) string {
	return "Model '" + requestedModel + "' is retired or scheduled for sunset by Anthropic" +
		" and has been removed from this TokenKey deployment. Please migrate to '" +
		replacement + "' (or '" + tkDeprecatedAnthropicReplacementOpus +
		"' for the Opus tier). See https://docs.anthropic.com/en/docs/about-claude/models/all-models" +
		" for the current model list."
}

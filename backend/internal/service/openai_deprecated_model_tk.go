package service

import (
	"errors"
	"fmt"
	"strings"
)

// TokenKey: friendly 400 gate for retired / non-selectable OpenAI model IDs.
//
// Mirrors gateway_anthropic_deprecated_model_tk.go's ErrDeprecatedAnthropicModel
// gate for the OpenAI-compat surface. Without this gate, requests for these ids
// only fail after account selection exhausts the pool (ErrUnsupportedModel, or a
// plain empty-pool 429), which reads as a generic failure instead of a clear
// retirement/non-selectable notice.
//
// Retired 2026-07 (SSOT audit directives #2/#5):
//   - gpt-5.2, gpt-5.2-pro, gpt-5.3-codex, gpt-5-codex: superseded generations,
//     removed from the servable allowlist (pricing_catalog_supported_models_tk.go).
//   - codex-auto-review: never a directly selectable model — an internal Codex
//     capability id that leaked into some client configs; not a version
//     retirement, so it carries an empty replacement (see the message-branch
//     split in TkBuildDeprecatedOpenAIModelMessage).
//
// Only the account-selection-failure path is wired (openAICompatNoCandidateError /
// tkSelectFailureStatusMessage) — that single choke point already covers every
// OpenAI-compat endpoint (chat/completions, responses, and the Anthropic-shaped
// /v1/messages hosted on OpenAIGatewayHandler), so no per-endpoint ingress
// fast-fail is needed.

// TkDeprecatedOpenAIErrorType — OpenAI-compat error envelope's "type" field for a
// deprecated/non-selectable model rejection.
const TkDeprecatedOpenAIErrorType = "invalid_request_error"

// ErrDeprecatedOpenAIModel reports that account selection failed while the
// requested model ID is on TokenKey's retired/non-selectable OpenAI list. This is
// a client error (HTTP 400), not a routing-capacity 429 — mirrors
// ErrDeprecatedAnthropicModel's rationale.
//
// Deliberately omits "no available accounts" so handler.isOpsNoAvailableAccountError
// does not relabel it as routing-capacity.
var ErrDeprecatedOpenAIModel = errors.New("deprecated openai model")

// tkDeprecatedOpenAIModels maps every retired or non-selectable OpenAI model ID to
// its recommended replacement. The map is the single source of truth for both the
// deprecation gate below and the matching unit test. An empty replacement marks an
// id that was never a standalone selectable model (codex-auto-review) rather than
// a version retirement — see TkBuildDeprecatedOpenAIModelMessage's message split.
var tkDeprecatedOpenAIModels = map[string]string{
	"gpt-5.2":           "gpt-5.5",
	"gpt-5.2-pro":       "gpt-5.5-pro",
	"gpt-5.3-codex":     "gpt-5.3-codex-spark",
	"gpt-5-codex":       "gpt-5.3-codex-spark",
	"codex-auto-review": "",
}

// tkIsDeprecatedOpenAIModel reports whether the model ID is on the retired/
// non-selectable list, and returns the recommended replacement (empty string for
// the "never selectable" case). Lookups are exact-match.
func tkIsDeprecatedOpenAIModel(model string) (string, bool) {
	if model == "" {
		return "", false
	}
	replacement, ok := tkDeprecatedOpenAIModels[model]
	return replacement, ok
}

// TkLookupDeprecatedOpenAIModel resolves a client model name against the retired
// table, trying the raw id and CanonicalizeOpenAICompatRoutingModel (the same
// normalization the OpenAI-compat scheduler uses for account matching). Returns
// the matched table key for messaging.
func TkLookupDeprecatedOpenAIModel(requestedModel string) (matchedModel, replacement string, ok bool) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return "", "", false
	}
	if replacement, ok := tkIsDeprecatedOpenAIModel(requestedModel); ok {
		return requestedModel, replacement, true
	}
	if normalized := CanonicalizeOpenAICompatRoutingModel(requestedModel); normalized != "" && normalized != requestedModel {
		if replacement, ok := tkIsDeprecatedOpenAIModel(normalized); ok {
			return normalized, replacement, true
		}
	}
	return "", "", false
}

// tkDeprecatedOpenAISelectionFailure wraps ErrDeprecatedOpenAIModel when the
// requested model is retired/non-selectable. Used at account-selection failure
// (openAICompatNoCandidateError) so routing never surfaces a retry-storm 429 or a
// generic 400 for a name that can never succeed as sent.
func tkDeprecatedOpenAISelectionFailure(requestedModel string) error {
	matched, replacement, ok := TkLookupDeprecatedOpenAIModel(requestedModel)
	if !ok {
		return nil
	}
	if replacement == "" {
		return fmt.Errorf("%w: %s (not directly selectable)", ErrDeprecatedOpenAIModel, matched)
	}
	return fmt.Errorf("%w: %s (suggest %q)", ErrDeprecatedOpenAIModel, matched, replacement)
}

// TkBuildDeprecatedOpenAIModelMessage assembles the friendly client-facing
// message. Kept as a separate function so the unit test can assert message
// content without parsing JSON.
func TkBuildDeprecatedOpenAIModelMessage(requestedModel, replacement string) string {
	if replacement == "" {
		return "Model '" + requestedModel + "' is an internal capability and is not directly selectable." +
			" Choose an explicit model id such as 'gpt-5.4' or 'gpt-5.3-codex'."
	}
	return "Model '" + requestedModel + "' is retired and has been removed from this TokenKey deployment." +
		" Please migrate to '" + replacement + "'."
}

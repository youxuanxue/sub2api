package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// TokenKey: friendly 400 gate for retired / never-selectable OpenAI model IDs.
// Mirrors gateway_anthropic_deprecated_model_tk.go's design for the OpenAI-compat
// surface (chat/completions, /v1/responses, /v1/messages-shaped OpenAI requests).
//
// Why this exists: 2026-07 SSOT audit directive #2/#5. Without this gate, a
// request for one of these IDs falls through to the generic "no available
// accounts" 429/400 once every pool candidate rejects the name, which reads as
// a capacity problem rather than "this id can never work." This gate returns a
// clear client-facing 400 with a migration suggestion instead.
//
// Table keys are RAW client-facing model names. gpt-5.2-pro is silently
// rewritten to gpt-5.2 by the routing-alias substring fallback
// (canonicalizeOpenAIModelAliasSpelling / normalizeKnownOpenAICodexModel)
// before account selection ever sees it, so both "gpt-5.2" and "gpt-5.2-pro"
// are listed as independent exact-match keys — the collapsed canonical form
// ("gpt-5.2") is itself a key, so the gate fires correctly regardless of
// whether the call site has the raw or the canonicalized model in hand.
//
// codex-auto-review is not a version-retired id — it is an internal review
// capability that was never meant to be directly selectable by clients. It
// gets the same 400 treatment but a different message (empty replacement).
//
// gpt-5.2*'s suggested replacement is gpt-5.5 (NOT gpt-5.5-pro): as of this
// gate's introduction, gpt-5.5-pro has not yet been live-probed back into
// supportedOpenAICatalogModels (SSOT audit directive #7, phase 5). Recommending
// an unconfirmed-servable model in a client-facing error message would repeat
// the exact class of bug this audit exists to fix. Update this constant to
// tkDeprecatedOpenAIReplacementGPT55Pro once directive #7 lands gpt-5.5-pro.
const (
	tkDeprecatedOpenAIReplacementGPT55 = "gpt-5.5"
	// TkDeprecatedOpenAIErrorType — OpenAI-compat error envelope's inner type.
	TkDeprecatedOpenAIErrorType = "invalid_request_error"
)

// ErrDeprecatedOpenAIModel reports that account selection failed while the
// requested model ID is on TokenKey's retired/non-selectable OpenAI list.
// This is a client error (HTTP 400), not a routing-capacity 429 — see
// ErrDeprecatedAnthropicModel's comment for the identical prod-incident
// rationale (empty-pool 429 misread as a capacity signal).
var ErrDeprecatedOpenAIModel = errors.New("deprecated openai model")

// tkDeprecatedOpenAIModels maps every retired or never-selectable OpenAI model
// ID to its recommended TokenKey replacement. Empty string means "no
// replacement — this id is an internal capability, not a product tier";
// TkBuildDeprecatedOpenAIModelMessage renders that case with different text.
var tkDeprecatedOpenAIModels = map[string]string{
	"gpt-5.2":           tkDeprecatedOpenAIReplacementGPT55,
	"gpt-5.2-pro":       tkDeprecatedOpenAIReplacementGPT55,
	"codex-auto-review": "",
}

// tkIsDeprecatedOpenAIModel reports whether the model ID is on the retired/
// non-selectable list, and returns the recommended replacement (possibly "").
func tkIsDeprecatedOpenAIModel(model string) (string, bool) {
	if model == "" {
		return "", false
	}
	replacement, ok := tkDeprecatedOpenAIModels[model]
	return replacement, ok
}

func tkLookupDeprecatedOpenAIModelWithoutRouting(model string) (matchedModel, replacement string, ok bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", "", false
	}
	candidates := []string{model}
	if canonical := canonicalizeOpenAIModelAliasSpelling(model); canonical != "" && canonical != model {
		candidates = append(candidates, canonical)
	}
	for _, candidate := range candidates {
		if replacement, ok := tkIsDeprecatedOpenAIModel(candidate); ok {
			return candidate, replacement, true
		}
	}
	return "", "", false
}

// TkLookupDeprecatedOpenAIModel resolves a client model name against the
// retired table, trying the raw id and its CanonicalizeOpenAICompatRoutingModel
// form (same order as account selection). Returns the matched table key for
// messaging — mirrors TkLookupDeprecatedAnthropicModel's raw+normalized order.
func TkLookupDeprecatedOpenAIModel(requestedModel string) (matchedModel, replacement string, ok bool) {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return "", "", false
	}
	if matched, replacement, ok := tkLookupDeprecatedOpenAIModelWithoutRouting(requestedModel); ok {
		return matched, replacement, true
	}
	if canonical := CanonicalizeOpenAICompatRoutingModel(requestedModel); canonical != "" && canonical != requestedModel {
		if matched, replacement, ok := tkLookupDeprecatedOpenAIModelWithoutRouting(canonical); ok {
			return matched, replacement, true
		}
	}
	return "", "", false
}

// tkDeprecatedOpenAISelectionFailure wraps ErrDeprecatedOpenAIModel when the
// requested model is retired/non-selectable. Used at account-selection failure
// so routing never surfaces a retry-storm 429/generic 400 for a name that can
// never succeed as sent.
func tkDeprecatedOpenAISelectionFailure(requestedModel string) error {
	if matched, replacement, ok := TkLookupDeprecatedOpenAIModel(requestedModel); ok {
		return fmt.Errorf("%w: %s (suggest %q)", ErrDeprecatedOpenAIModel, matched, replacement)
	}
	return nil
}

// TkBuildDeprecatedOpenAIModelMessage assembles the friendly migration message.
// replacement=="" renders the "internal capability, not directly selectable"
// text (codex-auto-review); otherwise it renders the standard retirement text.
func TkBuildDeprecatedOpenAIModelMessage(requestedModel, replacement string) string {
	if replacement == "" {
		return "Model '" + requestedModel + "' is an internal capability and is not directly selectable." +
			" Choose an explicit model id such as gpt-5.4 or gpt-5.3-codex-spark."
	}
	return "Model '" + requestedModel + "' is retired and has been removed from this TokenKey deployment." +
		" Please migrate to '" + replacement + "'."
}

// TkWriteOpenAIDeprecatedModelError emits the OpenAI-compat-shape 400 error for
// a retired/non-selectable model request and aborts further gin handling. Safe
// to call with c == nil (no-op).
func TkWriteOpenAIDeprecatedModelError(c *gin.Context, requestedModel, replacement string) {
	if c == nil {
		return
	}
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
		"error": gin.H{
			"type":    TkDeprecatedOpenAIErrorType,
			"message": TkBuildDeprecatedOpenAIModelMessage(requestedModel, replacement),
		},
	})
}

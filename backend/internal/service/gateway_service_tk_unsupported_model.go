package service

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupportedModel reports that account selection failed solely because the
// requested model NAME is not served by any account in the pool — a caller
// (client) error, not a provider rate limit or a transient capacity gap.
//
// Prod incident 2026-06-06 (user_id=16): a client sent the bare model name
// "opus" (instead of the full "claude-opus-4-8"). No account's model_mapping
// allowlist matched "opus" and there is no opus→claude-opus-4-8 alias
// normalization (claude.NormalizeModelID only covers three short names), so the
// scheduler filtered every candidate as model_unsupported and returned
// ErrNoAvailableAccounts. On the prod→edge relay topology that surfaced to the
// client as a 429 "rate_limit_error" (handleUpstreamErrorResponse case 429),
// reading as "Anthropic is rate-limiting you" when the truth is "you asked for a
// model name nobody serves". 103 such requests also drove a wasteful failover
// storm.
//
// This sentinel is deliberately SEPARATE from ErrNoAvailableAccounts and its
// message deliberately omits the substring "no available accounts" — otherwise
// handler.isOpsNoAvailableAccountError (which matches that phrase) would relabel
// it as routing-capacity instead of a client request error.
var ErrUnsupportedModel = errors.New("unsupported model")

// TkUnsupportedModelErrType / TkUnsupportedModelMessage are the SINGLE SOURCE of
// the client-facing "unserveable model" contract, shared by the two emission
// points so they return a byte-identical response:
//   - path A (whitelist accounts): scheduler selection fails with model_unsupported
//     → handler.tkWriteUnsupportedModelIfApplicable;
//   - path B (passthrough accounts): the model is forwarded and the upstream
//     answers 404 model-not-found → service.handleErrorResponse (prod P0
//     2026-06-06 edge us5: bare "opus" on empty-mapping OAuth accounts).
//
// Both surface 400 invalid_request_error "Unsupported model: <name>", classified
// client-owned (phase=request) and kept out of upstream_error_rate.
const TkUnsupportedModelErrType = "invalid_request_error"

// TkUnsupportedModelMessage builds the client-facing message for an unserveable
// model name (single source so path A and path B never drift).
func TkUnsupportedModelMessage(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "Unsupported model"
	}
	return "Unsupported model: " + model
}

// tkSelectionFailedDueToUnsupportedModel reports whether a failed account
// selection was caused PURELY by "no account serves this model name", as opposed
// to a transient capacity gap.
//
// It is true only when every online, platform-matched candidate was rejected
// solely because it does not support the requested model, and there is NO:
//   - account that supports the model but is currently model-rate-limited
//     (ModelRateLimited) — that would be capacity, retry later; and no
//   - account that is currently unschedulable (Unschedulable) — those are
//     filtered BEFORE the model check (see diagnoseSelectionFailure ordering),
//     so any of them might support the model once it recovers.
//
// Any such noise → return false so the caller falls back to the original
// ErrNoAvailableAccounts/429 path (no misclassification, no regression). Eligible
// is 0 by construction at the failure point; asserted for safety.
func tkSelectionFailedDueToUnsupportedModel(stats selectionFailureStats) bool {
	return stats.ModelUnsupported > 0 &&
		stats.ModelRateLimited == 0 &&
		stats.Unschedulable == 0 &&
		stats.Eligible == 0
}

// tkWrapSelectionFailure is the single exit point for the two terminal
// "selected == nil" branches in SelectAccountWithLoadAwareness. It returns:
//   - ErrNoAvailableAccounts as-is when no model was requested;
//   - ErrUnsupportedModel (with the model name + stats) when the failure is
//     purely an unsupported model name (caller fault → handler maps to HTTP 400);
//   - otherwise the original ErrNoAvailableAccounts wrapped with the model + stats
//     (transient capacity → 429), preserving prior behavior.
func tkWrapSelectionFailure(requestedModel string, stats selectionFailureStats) error {
	if requestedModel == "" {
		return ErrNoAvailableAccounts
	}
	if tkSelectionFailedDueToUnsupportedModel(stats) {
		return fmt.Errorf("%w: %s (%s)", ErrUnsupportedModel, requestedModel, summarizeSelectionFailureStats(stats))
	}
	return fmt.Errorf("%w supporting model: %s (%s)", ErrNoAvailableAccounts, requestedModel, summarizeSelectionFailureStats(stats))
}

// tkIsForwardableAnthropicModelName reports whether a (normalized) model name is
// structurally inside the Anthropic namespace, i.e. safe to forward to
// api.anthropic.com on a direct-Anthropic account.
//
// Why this exists (prod 2026-06-16, edge us3 account oh1-ls-b ID 4): a
// passthrough (empty model_mapping) anthropic OAuth account forwards the client's
// raw model name verbatim, but the upstream only serves claude-*. Cross-vendor
// names (deepseek-/gpt-/gemini-/qwen- …) forwarded there always 404 AND are an
// abuse-detection fingerprint anomaly — a real Claude Code client never asks
// api.anthropic.com for "deepseek-v4-flash". The revoked account had sent
// deepseek-v4-flash; the same-edge survivor account (oh1-ls-a) never sent a
// cross-vendor name, so cross-vendor is the differentiated signal. A false
// verdict here routes the request to the existing Path A local 400
// (ErrUnsupportedModel) so the dirty name never leaves the gateway.
//
// Deliberately a namespace-prefix check, NOT a servable-catalog membership check:
// any future claude-* model forwards with zero code edits (forward-compat), with
// no stale-allowlist availability risk on a new-model launch day. Same-family
// stale/typo names (claude-haiku-4-6) are intentionally allowed through — the
// survivor sent claude-haiku-4-6 16× over 12 days and was NOT revoked, so the
// upstream tolerates them; they are not worth the staleness cost to block.
func tkIsForwardableAnthropicModelName(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true // empty model name is out of this guard's scope (handled elsewhere)
	}
	return strings.HasPrefix(m, "claude-")
}

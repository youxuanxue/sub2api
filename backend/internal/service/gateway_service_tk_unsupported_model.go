package service

import (
	"errors"
	"fmt"
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

package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// TokenKey: thin-pool failover-exclusion guard.
//
// On a single-account ("thin") pool, a transient upstream stream error —
// typically Anthropic emitting a mid-stream overloaded_error/rate_limit_error
// that TokenKey wraps as a 502 UpstreamFailoverError with
// RetryableOnSameAccount=false — gets the lone account ADDED to the request's
// failover exclusion set (FailedAccountIDs) without any account-level cooldown.
// With only one account in the pool, the next candidate selection finds it
// excluded, the candidate list is empty, and selection returns
// ErrNoAvailableAccounts. That surfaces to the client as a misleading 429
// "No available accounts" — even though the account is perfectly healthy (not
// cooled down, windows not full) and the blip was an upstream-transient event.
//
// Production evidence (prod opus, 4h window): ~82% of opus errors were this
// self-inflicted empty-pool artifact (the #845 WARN reads
// total_accounts=1, filtered_excluded=1, everything else 0), vs 0 real 529 and
// 1 real 429. Cross-relay research (CRS / LiteLLM / new-api) shows TokenKey is
// the most aggressive of its peers here; LiteLLM explicitly does NOT cool down a
// single-deployment model group on 429 to avoid exactly this empty pool.
//
// This guard detects "candidate pool emptied SOLELY by failover exclusion" and
// returns a distinct sentinel (ErrThinPoolAllExcluded) so the handler retries
// after a short backoff (clearing the exclusion set) instead of fast-failing with
// a synthetic 429. It is deliberately narrow: if ANY other filter removed an
// account (cooldown/unschedulable, quota, RPM, window cost, model scope,
// platform), the guard does NOT fire — a real authoritative rate-limit cooldown
// is respected, not bypassed. Retries are bounded by FailoverState.MaxSwitches;
// on exhaustion the handler surfaces the real last upstream status, not a
// synthetic "No available accounts" 429.
//
// Generalized beyond single-account pools (prod 2026-06, the cc-us2/cc-us4
// incident): the original guard was capped to 1-account (SPOF) pools, on the
// assumption that in a 2+ pool any second-account unavailability would surface in
// the non-exclusion counters. That is FALSE for CORRELATED failover — when both
// stubs of a 2-pool forward to edges that are simultaneously dry, BOTH return the
// header-less capacity 429 and BOTH land in filtered_excluded (nothing else
// removes them), so the pool empties with filtered_excluded == len(accounts) and
// every other gate 0, yet the old size cap refused to rescue it and the client
// got a synthetic 429 (prod 12h: 893× total=2/excl=2, plus N=3..5). The
// all-excluded-by-failover-only predicate is the correct, size-INDEPENDENT signal
// (a genuine cooldown/quota removal still shows up in otherFiltersSum>0 and
// declines the guard for ANY N), so the size ceiling is removed. Operator-
// disabling one stub "fixed" the incident precisely because it collapsed the pool
// to N=1 where the old guard already fired; this makes the N≥2 case behave the
// same. Retries stay bounded by FailoverState.MaxSwitches regardless of N.

// ErrThinPoolAllExcluded signals that the load-balance candidate pool is empty
// solely because a thin pool's account(s) were excluded by THIS request's
// failover loop — not because of cooldown/unschedulable, quota, RPM, window
// cost, model scope, or platform filtering. The handler treats it as retryable
// (same-account backoff via HandleSelectionExhausted) rather than a
// client-facing 429.
var ErrThinPoolAllExcluded = errors.New("thin pool all excluded")

// SettingKeyThinPoolTransientRetryEnabled toggles the thin-pool guard. Default
// is ENABLED (the new, strictly safer behavior). Set the value to "false" to
// restore the upstream fast-fail-429 behavior as a kill switch.
const SettingKeyThinPoolTransientRetryEnabled = "tk_thin_pool_transient_retry_enabled"

// IsThinPoolTransientRetryEnabled reports whether the thin-pool guard is active.
// Fail-open: defaults to true unless an operator explicitly sets the value to a
// parseable falsey string ("false"/"0"). An unset/empty/garbage value keeps the
// safer default-on behavior.
func (s *SettingService) IsThinPoolTransientRetryEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyThinPoolTransientRetryEnabled)
	if err != nil {
		return true
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return true
	}
	enabled, perr := strconv.ParseBool(trimmed)
	if perr != nil {
		return true
	}
	return enabled
}

// tkThinPoolAllExcluded reports whether an empty load-balance candidate pool was
// caused ONLY by THIS request's failover exclusion, so the excluded account(s)
// should be retried (after backoff) rather than fast-failing with a client 429.
// Despite the historical "ThinPool" name it now applies to ANY pool size (see the
// file header): the size ceiling was removed because correlated failover empties a
// 2+ pool just as it does a single-account one.
//
// totalAccounts is the schedulable pool size (len(accounts) before filtering);
// filteredExcluded is the count removed because they were in the request's
// exclusion set; otherFiltersSum is the sum of every OTHER per-gate filter
// counter (unschedulable + platform + model mapping + model scope + quota +
// window cost + RPM). The guard fires only when at least one account was excluded
// AND nothing else removed any account — i.e. the pool emptied purely by failover
// exclusion. (This helper is only consulted on the empty-candidate path, where
// otherFiltersSum==0 implies filtered_excluded == totalAccounts, so this is
// exactly "all excluded by failover".) Retries stay bounded by
// FailoverState.MaxSwitches regardless of pool size.
func (s *GatewayService) tkThinPoolAllExcluded(ctx context.Context, totalAccounts, filteredExcluded, otherFiltersSum int) bool {
	if s == nil || s.settingService == nil {
		return false
	}
	if !s.settingService.IsThinPoolTransientRetryEnabled(ctx) {
		return false
	}
	if totalAccounts <= 0 {
		return false
	}
	return filteredExcluded > 0 && otherFiltersSum == 0
}

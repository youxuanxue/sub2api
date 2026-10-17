package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

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

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
// This guard detects "candidate pool emptied SOLELY by failover exclusion on a
// thin pool" and returns a distinct sentinel (ErrThinPoolAllExcluded) so the
// handler retries the same account after a short backoff (clearing the exclusion
// set) instead of fast-failing with a synthetic 429. It is deliberately narrow:
// if ANY other filter removed an account (cooldown/unschedulable, quota, RPM,
// window cost, model scope, platform), the guard does NOT fire — a real
// authoritative rate-limit cooldown is respected, not bypassed. Retries are
// bounded by FailoverState.MaxSwitches; on exhaustion the handler surfaces the
// real last upstream status, not a synthetic "No available accounts" 429.

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

// SettingKeyThinPoolMaxAccounts overrides defaultThinPoolMaxAccounts (the pool
// size at/under which the guard applies).
const SettingKeyThinPoolMaxAccounts = "tk_thin_pool_max_accounts"

// defaultThinPoolMaxAccounts is the schedulable-pool-size ceiling at/under which
// the guard applies. 1 = only single-account (SPOF) pools — the dominant and
// safest case (one account cannot starve siblings by being retried).
const defaultThinPoolMaxAccounts = 1

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

// ThinPoolMaxAccounts returns the configured pool-size ceiling for the guard
// (default defaultThinPoolMaxAccounts). Invalid/absent values fall back to the
// default; values < 1 are clamped to the default.
func (s *SettingService) ThinPoolMaxAccounts(ctx context.Context) int {
	if s == nil || s.settingRepo == nil {
		return defaultThinPoolMaxAccounts
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyThinPoolMaxAccounts)
	if err != nil {
		return defaultThinPoolMaxAccounts
	}
	n, perr := strconv.Atoi(strings.TrimSpace(raw))
	if perr != nil || n < 1 {
		return defaultThinPoolMaxAccounts
	}
	return n
}

// tkThinPoolAllExcluded reports whether an empty load-balance candidate pool was
// caused ONLY by failover exclusion on a thin pool, so the lone account should
// be retried (after backoff) rather than fast-failing with a client 429.
//
// totalAccounts is the schedulable pool size (len(accounts) before filtering);
// filteredExcluded is the count removed because they were in the request's
// exclusion set; otherFiltersSum is the sum of every OTHER per-gate filter
// counter (unschedulable + platform + model mapping + model scope + quota +
// window cost + RPM). The guard fires only when the pool is thin, at least one
// account was excluded, and nothing else removed any account.
func (s *GatewayService) tkThinPoolAllExcluded(ctx context.Context, totalAccounts, filteredExcluded, otherFiltersSum int) bool {
	if s == nil || s.settingService == nil {
		return false
	}
	if !s.settingService.IsThinPoolTransientRetryEnabled(ctx) {
		return false
	}
	if totalAccounts <= 0 || totalAccounts > s.settingService.ThinPoolMaxAccounts(ctx) {
		return false
	}
	return filteredExcluded > 0 && otherFiltersSum == 0
}

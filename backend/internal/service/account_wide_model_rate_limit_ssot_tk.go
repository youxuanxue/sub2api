package service

import (
	"sort"
	"time"
)

// AccountWideModelRateLimitThreshold is the shared SSOT for cascading many
// per-model cooldowns into an account-wide rate-limit block.
//
// Policy (conversation approval 2026-09-22): when an account has more than this
// many still-active model_rate_limits scopes (AICredits excluded), the whole
// account is treated as rate-limited for scheduling, admin/edge status, and
// list filters — every platform and account type. Read-time only; no write to
// accounts.rate_limit_reset_at is required for the cascade to take effect.
const AccountWideModelRateLimitThreshold = 3

// countedActiveModelRateLimits returns still-active model_rate_limits entries
// that participate in the account-wide cascade. AICredits is an overages meta
// key, not a model scope, and must not inflate the count.
func countedActiveModelRateLimits(account *Account, now time.Time) map[string]ActiveModelCooldown {
	active := account.ActiveModelRateLimits(now)
	if len(active) == 0 {
		return nil
	}
	out := make(map[string]ActiveModelCooldown, len(active))
	for scope, cooldown := range active {
		if scope == creditsExhaustedKey {
			continue
		}
		out[scope] = cooldown
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// accountWideModelRateLimitOverflowResetAt returns the earliest time at which
// enough model cooldowns expire for the active count to fall to
// AccountWideModelRateLimitThreshold. Nil when the cascade does not apply.
func accountWideModelRateLimitOverflowResetAt(account *Account, now time.Time) *time.Time {
	limits := countedActiveModelRateLimits(account, now)
	if len(limits) <= AccountWideModelRateLimitThreshold {
		return nil
	}
	resets := make([]time.Time, 0, len(limits))
	for _, cooldown := range limits {
		resets = append(resets, cooldown.RateLimitResetAt)
	}
	sort.Slice(resets, func(i, j int) bool { return resets[i].Before(resets[j]) })
	// Need (count - threshold) scopes to expire; unlock at that order statistic.
	idx := len(resets) - AccountWideModelRateLimitThreshold - 1
	reset := resets[idx]
	return &reset
}

// AccountWideRateLimitResetAt is the effective account-wide rate-limit end for
// scheduling and admin/edge projections. It unions the persisted column lock
// (when not a NewAPI false window lock) with the model-count cascade unlock.
func AccountWideRateLimitResetAt(account *Account, now time.Time) *time.Time {
	if account == nil {
		return nil
	}
	var latest *time.Time
	consider := func(t *time.Time) {
		if t == nil || !t.After(now) {
			return
		}
		if latest == nil || t.After(*latest) {
			cp := *t
			latest = &cp
		}
	}
	if account.RateLimitResetAt != nil && !newAPIAccountWindowLockIgnoredAt(account, now) {
		consider(account.RateLimitResetAt)
	}
	consider(accountWideModelRateLimitOverflowResetAt(account, now))
	return latest
}

// AccountWideRateLimitedAt surfaces a non-nil limited-at when the account is
// currently account-wide rate-limited. Prefer the persisted column when it is
// an authoritative lock; otherwise use the earliest counted model limited-at.
func AccountWideRateLimitedAt(account *Account, now time.Time) *time.Time {
	if account == nil || AccountWideRateLimitResetAt(account, now) == nil {
		return nil
	}
	if account.RateLimitedAt != nil && account.RateLimitResetAt != nil &&
		account.RateLimitResetAt.After(now) && !newAPIAccountWindowLockIgnoredAt(account, now) {
		return account.RateLimitedAt
	}
	limits := countedActiveModelRateLimits(account, now)
	var earliest *time.Time
	for _, cooldown := range limits {
		if cooldown.RateLimitedAt.IsZero() {
			continue
		}
		t := cooldown.RateLimitedAt
		if earliest == nil || t.Before(*earliest) {
			cp := t
			earliest = &cp
		}
	}
	return earliest
}

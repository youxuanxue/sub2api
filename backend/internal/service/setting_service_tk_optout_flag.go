package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// TK: shared read path for opt-out boolean kill-switches (default ON; only the
// literal value "false" disables).
//
// Why this exists: every accessor in this family used a single `if err != nil`
// branch around settingRepo.GetValue, which conflates two very different
// outcomes. settingRepo.GetValue returns ErrSettingNotFound for a key that was
// never written (see repository/setting_repo.go and its integration test), so a
// key that is simply absent — the normal state for a default-ON switch nobody
// has touched — was logged as a failure on every single read.
//
// Two consequences observed on prod (1.8.173, 2026-08-25):
//  1. Log noise: "failed to get openai/anthropic saturated-stub deprioritize
//     setting" accounted for 603 of 869 warn/error rows (69%) in a 50-minute
//     window, burying real signals.
//  2. A 12x read amplifier: the error branch caches for errorTTL (5s) instead of
//     okTTL (60s). That short TTL is correct for a transient DB fault, but for a
//     permanently-absent key it re-reads forever at 12x the intended rate.
//
// Resolved behavior is unchanged in every case — absent, empty, "false",
// garbage, and DB failure all produce the same enabled value as before. Only the
// logging and the cache TTL for the absent-key case change: absent is now
// treated as a successful read of the default, which is what the original
// comment in the anthropic accessor ("Empty string => never set => default
// true") already intended but could never reach, because the err != nil branch
// shadowed it.
//
// Real DB failures still warn and still use the short errorTTL, so an operator
// losing settings storage sees it immediately.
type tkOptOutFlagCacheEntry struct {
	enabled   bool
	expiresAt int64 // UnixNano
}

// tkResolveOptOutFlagValue maps one GetValue result to (enabled, ttl, warn).
//
// Split out as a pure function so the decision table is unit-testable without a
// SettingService, a repo stub, or process-level cache state:
//
//	absent (ErrSettingNotFound) → enabled=true,  ttl=okTTL,    warn=false
//	other error                 → enabled=true,  ttl=errorTTL, warn=true
//	"false"                     → enabled=false, ttl=okTTL,    warn=false
//	anything else (incl. "")    → enabled=true,  ttl=okTTL,    warn=false
func tkResolveOptOutFlagValue(raw string, err error, okTTL, errorTTL time.Duration) (enabled bool, ttl time.Duration, warn bool) {
	if err != nil {
		// A never-written key is the expected steady state for a default-ON
		// switch, not a fault. Cache it like a successful read of the default.
		if errors.Is(err, ErrSettingNotFound) {
			return true, okTTL, false
		}
		return true, errorTTL, true
	}
	return strings.TrimSpace(raw) != "false", okTTL, false
}

// tkOptOutFlagSpec is the per-flag wiring for one opt-out kill-switch. Each flag
// keeps its own cache slot and singleflight group so flags never share state.
type tkOptOutFlagSpec struct {
	key       string
	warnMsg   string
	cache     *atomic.Value // holds *tkOptOutFlagCacheEntry
	sf        *singleflight.Group
	okTTL     time.Duration
	errorTTL  time.Duration
	dbTimeout time.Duration
}

// tkReadOptOutFlag resolves one opt-out flag: process-level atomic cache,
// singleflight-collapsed DB read, fail-OPEN to true. warnMsg is emitted only for
// real storage failures, never for a never-written key.
func tkReadOptOutFlag(ctx context.Context, repo SettingRepository, spec tkOptOutFlagSpec) bool {
	if repo == nil {
		return true
	}
	if cached, ok := spec.cache.Load().(*tkOptOutFlagCacheEntry); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled
		}
	}
	val, _, _ := spec.sf.Do(spec.key, func() (any, error) {
		// Re-check under singleflight: a concurrent caller may have just filled it.
		if cached, ok := spec.cache.Load().(*tkOptOutFlagCacheEntry); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.enabled, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spec.dbTimeout)
		defer cancel()
		raw, err := repo.GetValue(dbCtx, spec.key)
		enabled, ttl, warn := tkResolveOptOutFlagValue(raw, err, spec.okTTL, spec.errorTTL)
		if warn {
			slog.Warn(spec.warnMsg, "error", err)
		}
		spec.cache.Store(&tkOptOutFlagCacheEntry{
			enabled:   enabled,
			expiresAt: time.Now().Add(ttl).UnixNano(),
		})
		return enabled, nil
	})
	if b, ok := val.(bool); ok {
		return b
	}
	return true
}

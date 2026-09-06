package service

import (
	"context"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// TK: opt-out kill-switch for Kiro local cache-billing (default ON; only the
// literal value "false" disables). Fail-open to enabled when settings storage
// is unavailable so multi-turn Kiro traffic keeps Anthropic-like cache_read
// pricing unless an operator explicitly turns it off.

var kiroCacheBillingCache atomic.Value // *tkOptOutFlagCacheEntry
var kiroCacheBillingSF singleflight.Group

const (
	kiroCacheBillingCacheTTL  = 60 * time.Second
	kiroCacheBillingErrorTTL  = 5 * time.Second
	kiroCacheBillingDBTimeout = 5 * time.Second
)

// IsKiroCacheBillingEnabled reports whether Kiro prompt tokens should be split
// into input + cache_read via local prefix fingerprints. Default true.
func (s *SettingService) IsKiroCacheBillingEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	return tkReadOptOutFlag(ctx, s.settingRepo, tkOptOutFlagSpec{
		key:       SettingKeyKiroCacheBillingEnabled,
		warnMsg:   "failed to get kiro cache billing setting",
		cache:     &kiroCacheBillingCache,
		sf:        &kiroCacheBillingSF,
		okTTL:     kiroCacheBillingCacheTTL,
		errorTTL:  kiroCacheBillingErrorTTL,
		dbTimeout: kiroCacheBillingDBTimeout,
	})
}

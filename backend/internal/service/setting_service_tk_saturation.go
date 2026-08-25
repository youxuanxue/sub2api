package service

import (
	"context"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// TK: kill-switch accessor for the anthropic saturated-stub de-prioritization
// preference (SettingKeyAnthropicSaturatedStubDeprioritizeEnabled). Mirrors
// IsStickyRoutingEnabled exactly in shape: process-level atomic cache (60s TTL),
// singleflight to collapse concurrent DB reads, fail-OPEN to true on missing
// key / DB error / unwired service. Default ON — an operator can disable the
// feature (settings → false) to fall back to pure priority/load selection.
//
// The read path itself lives in setting_service_tk_optout_flag.go; a
// never-written key resolves to the default without warning (see
// tkResolveOptOutFlagValue).

var satDeprioritizeCache atomic.Value // *tkOptOutFlagCacheEntry
var satDeprioritizeSF singleflight.Group

const satDeprioritizeCacheTTL = 60 * time.Second
const satDeprioritizeErrorTTL = 5 * time.Second
const satDeprioritizeDBTimeout = 5 * time.Second

// IsAnthropicSaturatedStubDeprioritizeEnabled reports whether the bounded
// saturation de-prioritization preference is active. Defaults to true.
func (s *SettingService) IsAnthropicSaturatedStubDeprioritizeEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	return tkReadOptOutFlag(ctx, s.settingRepo, tkOptOutFlagSpec{
		key:       SettingKeyAnthropicSaturatedStubDeprioritizeEnabled,
		warnMsg:   "failed to get anthropic saturated-stub deprioritize setting",
		cache:     &satDeprioritizeCache,
		sf:        &satDeprioritizeSF,
		okTTL:     satDeprioritizeCacheTTL,
		errorTTL:  satDeprioritizeErrorTTL,
		dbTimeout: satDeprioritizeDBTimeout,
	})
}

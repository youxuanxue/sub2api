package service

import (
	"context"
	"log/slog"
	"strings"
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

type satDeprioritizeCacheEntry struct {
	enabled   bool
	expiresAt int64 // UnixNano
}

var satDeprioritizeCache atomic.Value // *satDeprioritizeCacheEntry
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
	if cached, ok := satDeprioritizeCache.Load().(*satDeprioritizeCacheEntry); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled
		}
	}
	val, _, _ := satDeprioritizeSF.Do(SettingKeyAnthropicSaturatedStubDeprioritizeEnabled, func() (any, error) {
		if cached, ok := satDeprioritizeCache.Load().(*satDeprioritizeCacheEntry); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.enabled, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), satDeprioritizeDBTimeout)
		defer cancel()
		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyAnthropicSaturatedStubDeprioritizeEnabled)
		if err != nil {
			slog.Warn("failed to get anthropic saturated-stub deprioritize setting", "error", err)
			satDeprioritizeCache.Store(&satDeprioritizeCacheEntry{
				enabled:   true,
				expiresAt: time.Now().Add(satDeprioritizeErrorTTL).UnixNano(),
			})
			return true, nil
		}
		// Empty string => never set => default true (opt-out, not opt-in).
		enabled := strings.TrimSpace(raw) != "false"
		satDeprioritizeCache.Store(&satDeprioritizeCacheEntry{
			enabled:   enabled,
			expiresAt: time.Now().Add(satDeprioritizeCacheTTL).UnixNano(),
		})
		return enabled, nil
	})
	if b, ok := val.(bool); ok {
		return b
	}
	return true
}

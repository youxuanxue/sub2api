package service

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

type stickyRoutingCacheEntry struct {
	enabled   bool
	expiresAt int64 // UnixNano
}

var stickyRoutingCache atomic.Value // *stickyRoutingCacheEntry
var stickyRoutingSF singleflight.Group

const stickyRoutingCacheTTL = 60 * time.Second
const stickyRoutingErrorTTL = 5 * time.Second
const stickyRoutingDBTimeout = 5 * time.Second

type stickySlotFullEscapeCacheEntry struct {
	enabled   bool
	expiresAt int64 // UnixNano
}

var stickySlotFullEscapeCache atomic.Value // *stickySlotFullEscapeCacheEntry
var stickySlotFullEscapeSF singleflight.Group

const stickySlotFullEscapeCacheTTL = 60 * time.Second
const stickySlotFullEscapeErrorTTL = 5 * time.Second
const stickySlotFullEscapeDBTimeout = 5 * time.Second

// IsStickyRoutingEnabled reports whether prompt-cache sticky routing is active.
// It fails open to preserve gateway behavior when settings storage is unavailable.
func (s *SettingService) IsStickyRoutingEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	if cached, ok := stickyRoutingCache.Load().(*stickyRoutingCacheEntry); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled
		}
	}
	val, _, _ := stickyRoutingSF.Do(SettingKeyStickyRoutingEnabled, func() (any, error) {
		if cached, ok := stickyRoutingCache.Load().(*stickyRoutingCacheEntry); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.enabled, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stickyRoutingDBTimeout)
		defer cancel()
		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyStickyRoutingEnabled)
		if err != nil {
			slog.Warn("failed to get sticky routing setting", "error", err)
			stickyRoutingCache.Store(&stickyRoutingCacheEntry{
				enabled:   true,
				expiresAt: time.Now().Add(stickyRoutingErrorTTL).UnixNano(),
			})
			return true, nil
		}
		enabled := strings.TrimSpace(raw) != "false"
		stickyRoutingCache.Store(&stickyRoutingCacheEntry{
			enabled:   enabled,
			expiresAt: time.Now().Add(stickyRoutingCacheTTL).UnixNano(),
		})
		return enabled, nil
	})
	if b, ok := val.(bool); ok {
		return b
	}
	return true
}

// IsStickySlotFullEscapeEnabled reports whether a sticky OpenAI account whose
// concurrency slot is full may temporarily escape to another account. Defaults
// to true; only the literal setting value "false" disables it.
func (s *SettingService) IsStickySlotFullEscapeEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	if cached, ok := stickySlotFullEscapeCache.Load().(*stickySlotFullEscapeCacheEntry); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled
		}
	}
	val, _, _ := stickySlotFullEscapeSF.Do(SettingKeyStickySlotFullEscapeEnabled, func() (any, error) {
		if cached, ok := stickySlotFullEscapeCache.Load().(*stickySlotFullEscapeCacheEntry); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.enabled, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stickySlotFullEscapeDBTimeout)
		defer cancel()
		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyStickySlotFullEscapeEnabled)
		if err != nil {
			slog.Warn("failed to get sticky slot-full escape setting", "error", err)
			stickySlotFullEscapeCache.Store(&stickySlotFullEscapeCacheEntry{
				enabled:   true,
				expiresAt: time.Now().Add(stickySlotFullEscapeErrorTTL).UnixNano(),
			})
			return true, nil
		}
		enabled := strings.TrimSpace(raw) != "false"
		stickySlotFullEscapeCache.Store(&stickySlotFullEscapeCacheEntry{
			enabled:   enabled,
			expiresAt: time.Now().Add(stickySlotFullEscapeCacheTTL).UnixNano(),
		})
		return enabled, nil
	})
	if b, ok := val.(bool); ok {
		return b
	}
	return true
}

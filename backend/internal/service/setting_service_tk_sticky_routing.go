package service

import (
	"context"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// TK: kill-switch accessors for sticky routing and sticky slot-full escape. The
// shared read path lives in setting_service_tk_optout_flag.go and resolves a
// never-written key to the default without warning.

var stickyRoutingCache atomic.Value // *tkOptOutFlagCacheEntry
var stickyRoutingSF singleflight.Group

const stickyRoutingCacheTTL = 60 * time.Second
const stickyRoutingErrorTTL = 5 * time.Second
const stickyRoutingDBTimeout = 5 * time.Second

var stickySlotFullEscapeCache atomic.Value // *tkOptOutFlagCacheEntry
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
	return tkReadOptOutFlag(ctx, s.settingRepo, tkOptOutFlagSpec{
		key:       SettingKeyStickyRoutingEnabled,
		warnMsg:   "failed to get sticky routing setting",
		cache:     &stickyRoutingCache,
		sf:        &stickyRoutingSF,
		okTTL:     stickyRoutingCacheTTL,
		errorTTL:  stickyRoutingErrorTTL,
		dbTimeout: stickyRoutingDBTimeout,
	})
}

// IsStickySlotFullEscapeEnabled reports whether a sticky OpenAI account whose
// concurrency slot is full may temporarily escape to another account. Defaults
// to true; only the literal setting value "false" disables it.
func (s *SettingService) IsStickySlotFullEscapeEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	return tkReadOptOutFlag(ctx, s.settingRepo, tkOptOutFlagSpec{
		key:       SettingKeyStickySlotFullEscapeEnabled,
		warnMsg:   "failed to get sticky slot-full escape setting",
		cache:     &stickySlotFullEscapeCache,
		sf:        &stickySlotFullEscapeSF,
		okTTL:     stickySlotFullEscapeCacheTTL,
		errorTTL:  stickySlotFullEscapeErrorTTL,
		dbTimeout: stickySlotFullEscapeDBTimeout,
	})
}

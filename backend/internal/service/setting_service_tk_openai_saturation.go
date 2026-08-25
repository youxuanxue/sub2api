package service

import (
	"context"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// TK: kill-switch accessor for the openai saturated-stub de-prioritization
// preference (SettingKeyOpenAISaturatedStubDeprioritizeEnabled). Mirrors the
// anthropic accessor exactly in shape; the shared read path lives in
// setting_service_tk_optout_flag.go and resolves a never-written key to the
// default without warning.

var openaiSatDeprioritizeCache atomic.Value // *tkOptOutFlagCacheEntry
var openaiSatDeprioritizeSF singleflight.Group

const (
	openaiSatDeprioritizeCacheTTL  = 60 * time.Second
	openaiSatDeprioritizeErrorTTL  = 5 * time.Second
	openaiSatDeprioritizeDBTimeout = 5 * time.Second
)

// IsOpenAISaturatedStubDeprioritizeEnabled reports whether the bounded
// saturation de-prioritization preference is active. Defaults to true.
func (s *SettingService) IsOpenAISaturatedStubDeprioritizeEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	return tkReadOptOutFlag(ctx, s.settingRepo, tkOptOutFlagSpec{
		key:       SettingKeyOpenAISaturatedStubDeprioritizeEnabled,
		warnMsg:   "failed to get openai saturated-stub deprioritize setting",
		cache:     &openaiSatDeprioritizeCache,
		sf:        &openaiSatDeprioritizeSF,
		okTTL:     openaiSatDeprioritizeCacheTTL,
		errorTTL:  openaiSatDeprioritizeErrorTTL,
		dbTimeout: openaiSatDeprioritizeDBTimeout,
	})
}

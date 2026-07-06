package service

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	openaiSatDeprioritizeCacheTTL  = 60 * time.Second
	openaiSatDeprioritizeErrorTTL  = 5 * time.Second
	openaiSatDeprioritizeDBTimeout = 5 * time.Second
)

type openaiSatDeprioritizeCacheEntry struct {
	enabled   bool
	expiresAt int64
}

var openaiSatDeprioritizeCache atomic.Value // *openaiSatDeprioritizeCacheEntry
var openaiSatDeprioritizeSF singleflight.Group

func (s *SettingService) IsOpenAISaturatedStubDeprioritizeEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	if cached, ok := openaiSatDeprioritizeCache.Load().(*openaiSatDeprioritizeCacheEntry); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled
		}
	}
	val, _, _ := openaiSatDeprioritizeSF.Do(SettingKeyOpenAISaturatedStubDeprioritizeEnabled, func() (any, error) {
		if cached, ok := openaiSatDeprioritizeCache.Load().(*openaiSatDeprioritizeCacheEntry); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.enabled, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openaiSatDeprioritizeDBTimeout)
		defer cancel()
		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAISaturatedStubDeprioritizeEnabled)
		if err != nil {
			slog.Warn("failed to get openai saturated-stub deprioritize setting", "error", err)
			openaiSatDeprioritizeCache.Store(&openaiSatDeprioritizeCacheEntry{
				enabled:   true,
				expiresAt: time.Now().Add(openaiSatDeprioritizeErrorTTL).UnixNano(),
			})
			return true, nil
		}
		enabled := strings.TrimSpace(raw) != "false"
		openaiSatDeprioritizeCache.Store(&openaiSatDeprioritizeCacheEntry{
			enabled:   enabled,
			expiresAt: time.Now().Add(openaiSatDeprioritizeCacheTTL).UnixNano(),
		})
		return enabled, nil
	})
	if b, ok := val.(bool); ok {
		return b
	}
	return true
}

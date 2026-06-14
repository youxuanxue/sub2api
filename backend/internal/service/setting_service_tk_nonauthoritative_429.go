package service

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// TK: kill-switch accessor for the anthropic non-authoritative-429 failover
// behavior (SettingKeyAnthropicNonAuthoritative429Failover). Mirrors
// IsAnthropicSaturatedStubDeprioritizeEnabled exactly in shape: process-level
// atomic cache (60s TTL), singleflight to collapse concurrent DB reads, fail-OPEN
// to true on missing key / DB error / unwired service. Default ON — an operator
// running a DIRECT (non-mirror) Anthropic deployment can disable it (settings →
// false) to keep the conservative 5s fallback cooldown on header-less 429s.
// See ratelimit_service_tk_nonauthoritative_429.go for the classifier it gates.

type nonAuth429CacheEntry struct {
	enabled   bool
	expiresAt int64 // UnixNano
}

var nonAuth429Cache atomic.Value // *nonAuth429CacheEntry
var nonAuth429SF singleflight.Group

const nonAuth429CacheTTL = 60 * time.Second
const nonAuth429ErrorTTL = 5 * time.Second
const nonAuth429DBTimeout = 5 * time.Second

// IsAnthropicNonAuthoritative429FailoverEnabled reports whether a header-less
// Anthropic 429 should be treated as non-authoritative (fail over without
// cooldown / ladder). Defaults to true.
func (s *SettingService) IsAnthropicNonAuthoritative429FailoverEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	if cached, ok := nonAuth429Cache.Load().(*nonAuth429CacheEntry); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled
		}
	}
	val, _, _ := nonAuth429SF.Do(SettingKeyAnthropicNonAuthoritative429Failover, func() (any, error) {
		if cached, ok := nonAuth429Cache.Load().(*nonAuth429CacheEntry); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.enabled, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nonAuth429DBTimeout)
		defer cancel()
		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyAnthropicNonAuthoritative429Failover)
		if err != nil {
			slog.Warn("failed to get anthropic non-authoritative-429 failover setting", "error", err)
			nonAuth429Cache.Store(&nonAuth429CacheEntry{
				enabled:   true,
				expiresAt: time.Now().Add(nonAuth429ErrorTTL).UnixNano(),
			})
			return true, nil
		}
		// Empty string => never set => default true (opt-out, not opt-in).
		enabled := strings.TrimSpace(raw) != "false"
		nonAuth429Cache.Store(&nonAuth429CacheEntry{
			enabled:   enabled,
			expiresAt: time.Now().Add(nonAuth429CacheTTL).UnixNano(),
		})
		return enabled, nil
	})
	if b, ok := val.(bool); ok {
		return b
	}
	return true
}

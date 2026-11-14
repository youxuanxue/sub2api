package kiro

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Conservative Kiro cache-billing constants.
//
// Kiro upstream never reports Anthropic-style cache telemetry. TokenKey maps
// Kiro traffic onto Claude price tables, so multi-turn prompts were billed as
// full input. These knobs emulate Anthropic prefix-cache economics locally:
//
//   - MinCacheablePrefixTokens: Anthropic's practical minimum cacheable prefix.
//   - CacheReadHaircut: attribute 10% of a matched prefix to cache_read.
//     The other 90% stays input-priced.
//   - CacheCreationHaircut: attribute 100% of a new cacheable suffix to creation.
//     Settlement prices that aggregate at the unknown-TTL 1h write rate because
//     Kiro does not fill the 5m/1h token buckets.
//   - Default/Max TTL: 5m unless the request body asks for 1h cache_control.
const (
	MinCacheablePrefixTokens = 1024
	CacheReadHaircutBPS      = 1000  // 10.00% in basis points
	CacheCreationHaircutBPS  = 10000 // 100.00% in basis points
	DefaultCacheTTL          = 5 * time.Minute
	MaxCacheTTL              = time.Hour
)

// CacheUsageSplit is the prompt-side breakdown fed into CalculateCostUnified.
// Anthropic semantics: InputTokens is the non-cached remainder; total prompt
// tokens = Input + CacheCreation + CacheRead.
type CacheUsageSplit struct {
	InputTokens         int
	CacheCreationTokens int
	CacheReadTokens     int
	MatchedPrefixTokens int // pre-haircut matched prefix (observability)
}

// Total returns Input + Creation + Read.
func (s CacheUsageSplit) Total() int {
	return s.InputTokens + s.CacheCreationTokens + s.CacheReadTokens
}

type cachePrefixBlock struct {
	fingerprintHex string
	cumTokens      int
}

// CacheSessionKey builds the store namespace for one account conversation.
// conversationID is the Kiro ConversationState id (stable for same
// model+system+first-user-anchor). Empty conversationID disables matching.
func CacheSessionKey(accountID int64, model, conversationID string) string {
	conversationID = strings.TrimSpace(conversationID)
	if accountID <= 0 || conversationID == "" {
		return ""
	}
	return fmt.Sprintf("kiro_cache:%d:%s:%s", accountID, strings.TrimSpace(model), conversationID)
}

// EstimateCacheUsageSplit attributes prompt tokens for billing.
//
// When store/sessionKey is empty, the full estimate stays in InputTokens.
// Cacheable matches receive read attribution; newly written cacheable suffixes
// receive creation attribution, each subject to its own haircut.
// Call CommitCacheFingerprints after a successful upstream response.
func EstimateCacheUsageSplit(
	ctx context.Context,
	store CacheFingerprintStore,
	sessionKey string,
	req *ClaudeRequest,
) CacheUsageSplit {
	return PreparePromptEstimate(req).CacheUsageSplit(ctx, store, sessionKey)
}

// CommitCacheFingerprints records prefix fingerprints after a successful
// upstream response so the next turn in the same session can match them.
func CommitCacheFingerprints(
	ctx context.Context,
	store CacheFingerprintStore,
	sessionKey string,
	req *ClaudeRequest,
	ttl time.Duration,
) {
	PreparePromptEstimate(req).CommitCacheFingerprints(ctx, store, sessionKey, ttl)
}

// ResolveCacheTTL returns 1h when the raw Anthropic body requests ttl=1h,
// otherwise the default 5m TTL.
func ResolveCacheTTL(rawBody []byte) time.Duration {
	if bodyHasOneHourCacheControl(rawBody) {
		return MaxCacheTTL
	}
	return DefaultCacheTTL
}

func bodyHasOneHourCacheControl(rawBody []byte) bool {
	if len(rawBody) == 0 {
		return false
	}
	// Cheap structural probe — Claude Code emits compact JSON `"ttl":"1h"`.
	s := string(rawBody)
	return strings.Contains(s, `"ttl":"1h"`) ||
		strings.Contains(s, `"ttl": "1h"`) ||
		strings.Contains(s, `"ttl":"1H"`) ||
		strings.Contains(s, `"ttl": "1H"`)
}

func applyCacheReadHaircut(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens * CacheReadHaircutBPS / 10000
}

func applyCacheCreationHaircut(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens * CacheCreationHaircutBPS / 10000
}

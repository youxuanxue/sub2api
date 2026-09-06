package kiro

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
//   - CacheReadHaircut: bill only 90% of a matched prefix as cache_read so
//     tokenizer skew cannot systematically under-charge.
//   - Default/Max TTL: 5m unless the request body asks for 1h cache_control.
const (
	MinCacheablePrefixTokens = 1024
	CacheReadHaircutBPS      = 9000 // 90.00% in basis points
	DefaultCacheTTL          = 5 * time.Minute
	MaxCacheTTL              = time.Hour
)

// CacheUsageSplit is the prompt-side breakdown fed into CalculateCostUnified.
// Anthropic semantics: InputTokens is the non-cached remainder; total prompt
// tokens = Input + CacheCreation + CacheRead. V1 sets CacheCreation=0 and only
// applies the read discount (avoids charging 1.25× creation on first write).
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
// When store/sessionKey is empty, or the matched prefix is below
// MinCacheablePrefixTokens, the full estimate stays in InputTokens (no
// fabricated cache_read). On a successful match it applies CacheReadHaircutBPS.
// Call CommitCacheFingerprints after a successful upstream response.
func EstimateCacheUsageSplit(
	ctx context.Context,
	store CacheFingerprintStore,
	sessionKey string,
	req *ClaudeRequest,
) CacheUsageSplit {
	total := EstimateInputTokens(req)
	split := CacheUsageSplit{InputTokens: total}
	if total <= 0 || store == nil || strings.TrimSpace(sessionKey) == "" || req == nil {
		return split
	}

	blocks := buildCachePrefixBlocks(req)
	if len(blocks) == 0 {
		return split
	}

	matchedCum := 0
	for i := len(blocks) - 1; i >= 0; i-- {
		b := blocks[i]
		if b.cumTokens < MinCacheablePrefixTokens {
			continue
		}
		tokens, ok, err := store.Get(ctx, sessionKey, b.fingerprintHex)
		if err != nil || !ok {
			continue
		}
		// Prefer the stored token count when present; fall back to current estimate.
		if tokens > 0 {
			matchedCum = tokens
		} else {
			matchedCum = b.cumTokens
		}
		break
	}

	if matchedCum > 0 {
		// Align fingerprint-segment counts with EstimateInputTokens (labels differ).
		profileTotal := blocks[len(blocks)-1].cumTokens
		if profileTotal > 0 && profileTotal != total {
			matchedCum = matchedCum * total / profileTotal
		}
		if matchedCum > total {
			matchedCum = total
		}
	}

	if matchedCum >= MinCacheablePrefixTokens {
		read := applyCacheReadHaircut(matchedCum)
		if read > total {
			read = total
		}
		split.MatchedPrefixTokens = matchedCum
		split.CacheReadTokens = read
		split.InputTokens = total - read
	}
	return split
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
	if store == nil || strings.TrimSpace(sessionKey) == "" || req == nil {
		return
	}
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if ttl > MaxCacheTTL {
		ttl = MaxCacheTTL
	}
	for _, b := range buildCachePrefixBlocks(req) {
		if b.cumTokens <= 0 {
			continue
		}
		_ = store.Put(ctx, sessionKey, b.fingerprintHex, b.cumTokens, ttl)
	}
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

func buildCachePrefixBlocks(req *ClaudeRequest) []cachePrefixBlock {
	if req == nil {
		return nil
	}
	segments := make([]string, 0, 2+len(req.Messages))
	if sys := extractSystemPrompt(req.System); sys != "" {
		segments = append(segments, "system\n"+sys)
	}
	if tools := marshalToolsForCache(req.Tools); tools != "" {
		segments = append(segments, "tools\n"+tools)
	}
	for i := range req.Messages {
		msg := req.Messages[i]
		text := flattenMessageTextForCache(msg.Content)
		segments = append(segments, fmt.Sprintf("msg:%s:%d\n%s", msg.Role, i, text))
	}
	if len(segments) == 0 {
		return nil
	}

	prelude := sha256.Sum256([]byte("kiro-cache-v1|" + strings.TrimSpace(req.Model)))
	running := prelude
	cumTokens := 0
	out := make([]cachePrefixBlock, 0, len(segments))
	for _, seg := range segments {
		segHash := sha256.Sum256([]byte(seg))
		var next [32]byte
		h := sha256.New()
		_, _ = h.Write(running[:])
		_, _ = h.Write(segHash[:])
		copy(next[:], h.Sum(nil))
		running = next
		cumTokens += countTokens(seg)
		out = append(out, cachePrefixBlock{
			fingerprintHex: hex.EncodeToString(running[:]),
			cumTokens:      cumTokens,
		})
	}
	return out
}

func marshalToolsForCache(tools []ClaudeTool) string {
	if len(tools) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tools)*3)
	for i := range tools {
		t := tools[i]
		parts = append(parts, t.Name, t.Description)
		if js := marshalJSONForEstimate(t.InputSchema); js != "" {
			parts = append(parts, js)
		}
	}
	return strings.Join(parts, "\n")
}

func flattenMessageTextForCache(content any) string {
	parts := appendMessageContentForEstimate(nil, content)
	return strings.Join(parts, "\n")
}

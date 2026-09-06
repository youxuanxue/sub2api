//go:build unit

package kiro

import (
	"context"
	"strings"
	"testing"
	"time"
)

func largePrompt(n int) string {
	return strings.Repeat("alpha beta gamma delta ", n)
}

func TestEstimateCacheUsageSplit_FirstTurnNoRead(t *testing.T) {
	store := NewMemoryCacheFingerprintStore()
	req := &ClaudeRequest{
		Model:  "claude-sonnet-4-6",
		System: largePrompt(200),
		Messages: []ClaudeMessage{
			{Role: "user", Content: largePrompt(100)},
		},
	}
	session := CacheSessionKey(42, req.Model, "conv-stable")
	split := EstimateCacheUsageSplit(context.Background(), store, session, req)
	if split.CacheReadTokens != 0 {
		t.Fatalf("first turn cache_read=%d, want 0", split.CacheReadTokens)
	}
	if split.InputTokens != EstimateInputTokens(req) {
		t.Fatalf("first turn input=%d, want full estimate %d", split.InputTokens, EstimateInputTokens(req))
	}
	CommitCacheFingerprints(context.Background(), store, session, req, DefaultCacheTTL)
}

func TestEstimateCacheUsageSplit_SecondTurnSamePrefixHasRead(t *testing.T) {
	store := NewMemoryCacheFingerprintStore()
	session := CacheSessionKey(42, "claude-sonnet-4-6", "conv-stable")

	turn1 := &ClaudeRequest{
		Model:  "claude-sonnet-4-6",
		System: largePrompt(200),
		Messages: []ClaudeMessage{
			{Role: "user", Content: largePrompt(100)},
			{Role: "assistant", Content: "ack"},
		},
	}
	first := EstimateCacheUsageSplit(context.Background(), store, session, turn1)
	if first.CacheReadTokens != 0 {
		t.Fatalf("turn1 cache_read=%d, want 0", first.CacheReadTokens)
	}
	if got := EstimateInputTokens(turn1); got < MinCacheablePrefixTokens {
		t.Fatalf("fixture too small: estimate=%d, need >= %d", got, MinCacheablePrefixTokens)
	}
	CommitCacheFingerprints(context.Background(), store, session, turn1, DefaultCacheTTL)

	turn2 := &ClaudeRequest{
		Model:  "claude-sonnet-4-6",
		System: largePrompt(200),
		Messages: []ClaudeMessage{
			{Role: "user", Content: largePrompt(100)},
			{Role: "assistant", Content: "ack"},
			{Role: "user", Content: "continue with more context " + largePrompt(20)},
		},
	}
	second := EstimateCacheUsageSplit(context.Background(), store, session, turn2)
	if second.CacheReadTokens <= 0 {
		t.Fatalf("turn2 cache_read=%d, want > 0", second.CacheReadTokens)
	}
	if second.InputTokens+second.CacheReadTokens != EstimateInputTokens(turn2) {
		t.Fatalf("split total %d+%d != estimate %d", second.InputTokens, second.CacheReadTokens, EstimateInputTokens(turn2))
	}
	// Haircut: billed read must be strictly below the matched prefix.
	if second.CacheReadTokens >= second.MatchedPrefixTokens {
		t.Fatalf("haircut not applied: read=%d matched=%d", second.CacheReadTokens, second.MatchedPrefixTokens)
	}
}

func TestEstimateCacheUsageSplit_DifferentSessionNoRead(t *testing.T) {
	store := NewMemoryCacheFingerprintStore()
	req := &ClaudeRequest{
		Model:  "claude-sonnet-4-6",
		System: largePrompt(200),
		Messages: []ClaudeMessage{
			{Role: "user", Content: largePrompt(100)},
		},
	}
	CommitCacheFingerprints(context.Background(), store, CacheSessionKey(42, req.Model, "conv-a"), req, DefaultCacheTTL)

	split := EstimateCacheUsageSplit(context.Background(), store, CacheSessionKey(42, req.Model, "conv-b"), req)
	if split.CacheReadTokens != 0 {
		t.Fatalf("different session cache_read=%d, want 0", split.CacheReadTokens)
	}
}

func TestEstimateCacheUsageSplit_BelowMinPrefixStaysInput(t *testing.T) {
	store := NewMemoryCacheFingerprintStore()
	session := CacheSessionKey(7, "claude-sonnet-4-6", "tiny")
	req := &ClaudeRequest{
		Model: "claude-sonnet-4-6",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hi"},
		},
	}
	CommitCacheFingerprints(context.Background(), store, session, req, time.Minute)
	turn2 := &ClaudeRequest{
		Model: "claude-sonnet-4-6",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
			{Role: "user", Content: "again"},
		},
	}
	split := EstimateCacheUsageSplit(context.Background(), store, session, turn2)
	if split.CacheReadTokens != 0 {
		t.Fatalf("below-min prefix must not bill cache_read, got %d", split.CacheReadTokens)
	}
}

func TestResolveCacheTTL(t *testing.T) {
	if got := ResolveCacheTTL([]byte(`{"system":[{"cache_control":{"ttl":"1h"}}]}`)); got != MaxCacheTTL {
		t.Fatalf("ttl=%v, want 1h", got)
	}
	if got := ResolveCacheTTL([]byte(`{"system":"x"}`)); got != DefaultCacheTTL {
		t.Fatalf("ttl=%v, want 5m", got)
	}
}

func TestCacheSessionKey_EmptyConversationDisabled(t *testing.T) {
	if CacheSessionKey(1, "m", "") != "" {
		t.Fatal("empty conversation must disable session key")
	}
}

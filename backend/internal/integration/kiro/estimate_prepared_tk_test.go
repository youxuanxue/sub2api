//go:build unit

package kiro

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Frozen oracle: verbatim copy of the pre-memoization estimators (1.8.358).
// Token estimates feed billing, so the memoized path must match these
// byte-for-byte. Do not "fix" or simplify this copy.
// ---------------------------------------------------------------------------

func legacyEstimateInputTokens(req *ClaudeRequest) int {
	if req == nil {
		return 0
	}
	parts := make([]string, 0, 8)
	if sys := extractSystemPrompt(req.System); sys != "" {
		parts = append(parts, sys)
	}
	for i := range req.Messages {
		parts = appendMessageContentForEstimate(parts, req.Messages[i].Content)
	}
	total := countTokens(strings.Join(parts, "\n"))
	total += legacyEstimateToolsTokens(req.Tools)
	return total
}

func legacyEstimateToolsTokens(tools []ClaudeTool) int {
	if len(tools) == 0 {
		return 0
	}
	parts := make([]string, 0, len(tools)*3)
	for i := range tools {
		t := tools[i]
		if t.Name != "" {
			parts = append(parts, t.Name)
		}
		if t.Description != "" {
			parts = append(parts, t.Description)
		}
		if js := marshalJSONForEstimate(t.InputSchema); js != "" {
			parts = append(parts, js)
		}
	}
	return countTokens(strings.Join(parts, "\n"))
}

func legacyEstimateCacheUsageSplit(ctx context.Context, store CacheFingerprintStore, sessionKey string, req *ClaudeRequest) CacheUsageSplit {
	total := legacyEstimateInputTokens(req)
	split := CacheUsageSplit{InputTokens: total}
	if total <= 0 || store == nil || strings.TrimSpace(sessionKey) == "" || req == nil {
		return split
	}
	blocks := legacyBuildCachePrefixBlocks(req)
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
		if tokens > 0 {
			matchedCum = tokens
		} else {
			matchedCum = b.cumTokens
		}
		break
	}
	if matchedCum > 0 {
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

func legacyCommitCacheFingerprints(ctx context.Context, store CacheFingerprintStore, sessionKey string, req *ClaudeRequest, ttl time.Duration) {
	if store == nil || strings.TrimSpace(sessionKey) == "" || req == nil {
		return
	}
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if ttl > MaxCacheTTL {
		ttl = MaxCacheTTL
	}
	for _, b := range legacyBuildCachePrefixBlocks(req) {
		if b.cumTokens <= 0 {
			continue
		}
		_ = store.Put(ctx, sessionKey, b.fingerprintHex, b.cumTokens, ttl)
	}
}

func legacyBuildCachePrefixBlocks(req *ClaudeRequest) []cachePrefixBlock {
	if req == nil {
		return nil
	}
	segments := make([]string, 0, 2+len(req.Messages))
	if sys := extractSystemPrompt(req.System); sys != "" {
		segments = append(segments, "system\n"+sys)
	}
	if tools := legacyMarshalToolsForCache(req.Tools); tools != "" {
		segments = append(segments, "tools\n"+tools)
	}
	for i := range req.Messages {
		msg := req.Messages[i]
		text := strings.Join(appendMessageContentForEstimate(nil, msg.Content), "\n")
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

func legacyMarshalToolsForCache(tools []ClaudeTool) string {
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

// legacyPromptUsageFlow is the pre-change per-request sequence run by
// KiroGatewayService: kiroPromptUsage (EstimateInputTokens, then
// EstimateCacheUsageSplit) followed by commitKiroCacheFingerprints.
func legacyPromptUsageFlow(ctx context.Context, store CacheFingerprintStore, session string, req *ClaudeRequest) (int, CacheUsageSplit) {
	in := legacyEstimateInputTokens(req)
	split := legacyEstimateCacheUsageSplit(ctx, store, session, req)
	legacyCommitCacheFingerprints(ctx, store, session, req, DefaultCacheTTL)
	return in, split
}

// ---------------------------------------------------------------------------
// Realistic fixtures
// ---------------------------------------------------------------------------

const benchProse = "The gateway forwards Claude Code traffic to Kiro and must estimate usage locally. " +
	"请检查这个函数的边界条件，并说明为什么会出现重复计数。 " +
	"func (s *Service) Handle(ctx context.Context, req *Request) error {\n\tif req == nil {\n\t\treturn errNil\n\t}\n\treturn s.next(ctx, req)\n}\n" +
	"- item one: trailing spaces   \n- item two.\n\n```json\n{\"k\": [1, 2, 3], \"ok\": true}\n```\n"

func benchText(seed, approxBytes int) string {
	var b strings.Builder
	for i := 0; b.Len() < approxBytes; i++ {
		fmt.Fprintf(&b, "[%d.%d] ", seed, i)
		b.WriteString(benchProse)
	}
	return b.String()
}

func benchToolSchema(i int) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": fmt.Sprintf("Absolute file path for tool %d.", i)},
			"limit":   map[string]any{"type": "integer", "minimum": 1},
			"pattern": map[string]any{"type": "string", "description": "Regular expression to search for."},
			"options": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "enum": []any{"a", "b", "c"}},
			},
		},
		"required":             []any{"path"},
		"additionalProperties": false,
	}
}

// realisticKiroRequest builds a Claude Code-shaped multi-turn request: block
// system prompt, a tool catalog, and turns alternating text, tool_use and
// tool_result blocks. turns controls history length.
func realisticKiroRequest(turns int) *ClaudeRequest {
	tools := make([]ClaudeTool, 0, 18)
	for i := 0; i < 18; i++ {
		tools = append(tools, ClaudeTool{
			Name:        fmt.Sprintf("tool_%02d", i),
			Description: benchText(1000+i, 600),
			InputSchema: benchToolSchema(i),
		})
	}
	msgs := make([]ClaudeMessage, 0, turns*2)
	for t := 0; t < turns; t++ {
		switch t % 3 {
		case 0:
			msgs = append(msgs, ClaudeMessage{Role: "user", Content: benchText(t, 900)})
			msgs = append(msgs, ClaudeMessage{Role: "assistant", Content: []any{
				map[string]any{"type": "thinking", "thinking": "hidden"},
				map[string]any{"type": "text", "text": benchText(t+500, 400)},
				map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%d", t), "name": "tool_03",
					"input": map[string]any{"path": fmt.Sprintf("/repo/file_%d.go", t), "limit": 200, "options": []any{"a", "c"}}},
			}})
		case 1:
			msgs = append(msgs, ClaudeMessage{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("toolu_%d", t-1),
					"content": []any{map[string]any{"type": "text", "text": benchText(t+900, 2500)}}},
				map[string]any{"type": "text", "text": "继续。"},
			}})
			msgs = append(msgs, ClaudeMessage{Role: "assistant", Content: benchText(t+700, 300)})
		default:
			msgs = append(msgs, ClaudeMessage{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": benchText(t, 500)},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}},
			}})
			msgs = append(msgs, ClaudeMessage{Role: "assistant", Content: []any{
				map[string]any{"type": "text", "text": benchText(t+300, 200)},
			}})
		}
	}
	return &ClaudeRequest{
		Model: "claude-sonnet-4-6",
		System: []any{
			map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			map[string]any{"type": "text", "text": benchText(42, 9000), "cache_control": map[string]any{"type": "ephemeral"}},
		},
		Tools:    tools,
		Messages: msgs,
	}
}

func benchRequestBytes(req *ClaudeRequest) int {
	n := len(extractSystemPrompt(req.System))
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(marshalJSONForEstimate(t.InputSchema))
	}
	for _, m := range req.Messages {
		for _, p := range appendMessageContentForEstimate(nil, m.Content) {
			n += len(p)
		}
	}
	return n
}

func benchSeededStore(b *testing.B, req *ClaudeRequest) (CacheFingerprintStore, string) {
	b.Helper()
	store := NewMemoryCacheFingerprintStore()
	session := CacheSessionKey(7, req.Model, "conv-bench")
	prev := *req
	prev.Messages = req.Messages[:len(req.Messages)-2]
	legacyCommitCacheFingerprints(context.Background(), store, session, &prev, DefaultCacheTTL)
	return store, session
}

// BenchmarkKiroPromptUsageFlowLegacy measures the pre-change per-request
// estimate+split+commit sequence (frozen oracle) on a ~60 KB second-turn request.
func BenchmarkKiroPromptUsageFlowLegacy(b *testing.B) {
	req := realisticKiroRequest(24)
	store, session := benchSeededStore(b, req)
	b.SetBytes(int64(benchRequestBytes(req)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = legacyPromptUsageFlow(context.Background(), store, session, req)
	}
}

// equivalenceFixtures covers realistic Claude Code shapes plus edge cases that
// exercise each branch where the input and cache-segment strings differ.
func equivalenceFixtures() map[string]*ClaudeRequest {
	f := map[string]*ClaudeRequest{
		"nil-fields": {Model: "claude-sonnet-4-6"},
		"string-only": {Model: "claude-sonnet-4-6", System: "sys", Messages: []ClaudeMessage{
			{Role: "user", Content: "hello"},
		}},
		"empty-system-and-content": {Model: " claude-opus-4-7 ", System: "", Messages: []ClaudeMessage{
			{Role: "user", Content: ""},
			{Role: "assistant", Content: []any{}},
			{Role: "user", Content: 42},
		}},
		"tool-empty-name-desc": {Model: "claude-sonnet-4-6", Tools: []ClaudeTool{
			{Name: "", Description: "", InputSchema: nil},
			{Name: "x", Description: "", InputSchema: map[string]any{}},
			{Name: "", Description: "only desc", InputSchema: map[string]any{"type": "object"}},
			{Name: "y", Description: "d", InputSchema: []any{}},
		}, Messages: []ClaudeMessage{{Role: "user", Content: "hi"}}},
		"tool-use-empty-input": {Model: "claude-sonnet-4-6", Messages: []ClaudeMessage{
			{Role: "assistant", Content: []any{
				map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{}},
				map[string]any{"type": "tool_use", "id": "t2", "name": "", "input": nil},
				map[string]any{"type": "input_text", "text": "inline"},
				"not-a-map",
			}},
			{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "ok\r\n\r\n"},
				map[string]any{"type": "tool_result", "tool_use_id": "t2", "content": []any{
					map[string]any{"type": "text", "text": "a"},
					map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AA=="}},
					map[string]any{"type": "text", "text": "b"},
				}},
			}},
		}},
		"whitespace-boundaries": {Model: "claude-sonnet-4-6", System: "  \n", Messages: []ClaudeMessage{
			{Role: "user", Content: "end with spaces   "},
			{Role: "assistant", Content: "\n\nstart with newlines"},
			{Role: "user", Content: "数字123456和emoji🙂"},
		}},
	}
	for _, turns := range []int{1, 2, 3, 6, 12, 24, 40} {
		f[fmt.Sprintf("realistic-%d", turns)] = realisticKiroRequest(turns)
	}
	return f
}

// TestPromptEstimate_MatchesLegacyByteForByte proves the memoized path yields
// exactly the legacy input totals, prefix fingerprints, cumulative counts and
// cache splits, across first-turn, matched-prefix and stored-count cases.
func TestPromptEstimate_MatchesLegacyByteForByte(t *testing.T) {
	ctx := context.Background()
	matchedReads := 0
	for name, req := range equivalenceFixtures() {
		t.Run(name, func(t *testing.T) {
			want := legacyEstimateInputTokens(req)
			if got := EstimateInputTokens(req); got != want {
				t.Fatalf("EstimateInputTokens=%d legacy=%d", got, want)
			}
			est := PreparePromptEstimate(req)
			if got := est.InputTokens(); got != want {
				t.Fatalf("PromptEstimate.InputTokens=%d legacy=%d", got, want)
			}
			wantBlocks := legacyBuildCachePrefixBlocks(req)
			gotBlocks := est.prefixBlocks()
			if len(gotBlocks) != len(wantBlocks) {
				t.Fatalf("blocks len=%d legacy=%d", len(gotBlocks), len(wantBlocks))
			}
			for i := range wantBlocks {
				if gotBlocks[i] != wantBlocks[i] {
					t.Fatalf("block %d=%+v legacy=%+v", i, gotBlocks[i], wantBlocks[i])
				}
			}

			// Walk every possible matched prefix: commit a truncated history to
			// both stores, then compare the split for the full request.
			for cut := 0; cut <= len(req.Messages); cut++ {
				prev := *req
				prev.Messages = req.Messages[:cut]
				session := CacheSessionKey(9, req.Model, "conv-eq")
				legacyStore := NewMemoryCacheFingerprintStore()
				newStore := NewMemoryCacheFingerprintStore()
				legacyCommitCacheFingerprints(ctx, legacyStore, session, &prev, DefaultCacheTTL)
				PreparePromptEstimate(&prev).CommitCacheFingerprints(ctx, newStore, session, DefaultCacheTTL)

				wantSplit := legacyEstimateCacheUsageSplit(ctx, legacyStore, session, req)
				est := PreparePromptEstimate(req)
				gotSplit := est.CacheUsageSplit(ctx, newStore, session)
				if gotSplit != wantSplit {
					t.Fatalf("cut=%d split=%+v legacy=%+v", cut, gotSplit, wantSplit)
				}
				if wantSplit.CacheReadTokens > 0 {
					matchedReads++
				}
				if pkg := EstimateCacheUsageSplit(ctx, newStore, session, req); pkg != wantSplit {
					t.Fatalf("cut=%d EstimateCacheUsageSplit=%+v legacy=%+v", cut, pkg, wantSplit)
				}

				// Commit after split (forward order) must write identical entries.
				legacyCommitCacheFingerprints(ctx, legacyStore, session, req, time.Hour)
				est.CommitCacheFingerprints(ctx, newStore, session, time.Hour)
				for _, b := range wantBlocks {
					lt, lok, _ := legacyStore.Get(ctx, session, b.fingerprintHex)
					nt, nok, _ := newStore.Get(ctx, session, b.fingerprintHex)
					if lt != nt || lok != nok {
						t.Fatalf("cut=%d fp=%s store legacy=(%d,%v) new=(%d,%v)", cut, b.fingerprintHex, lt, lok, nt, nok)
					}
				}
			}
		})
	}
	if matchedReads < 20 {
		t.Fatalf("fixtures exercised only %d cache-read matches; equivalence coverage too weak", matchedReads)
	}
}

// TestPromptEstimate_StoredCountRescaled covers the branch where the stored
// token count differs from the current segment estimate.
func TestPromptEstimate_StoredCountRescaled(t *testing.T) {
	ctx := context.Background()
	req := realisticKiroRequest(9)
	session := CacheSessionKey(3, req.Model, "conv-rescale")
	blocks := legacyBuildCachePrefixBlocks(req)
	for _, stored := range []int{0, 1, MinCacheablePrefixTokens, blocks[len(blocks)/2].cumTokens + 777, 10_000_000} {
		legacyStore := NewMemoryCacheFingerprintStore()
		newStore := NewMemoryCacheFingerprintStore()
		fp := blocks[len(blocks)/2].fingerprintHex
		_ = legacyStore.Put(ctx, session, fp, stored, DefaultCacheTTL)
		_ = newStore.Put(ctx, session, fp, stored, DefaultCacheTTL)
		want := legacyEstimateCacheUsageSplit(ctx, legacyStore, session, req)
		if got := PreparePromptEstimate(req).CacheUsageSplit(ctx, newStore, session); got != want {
			t.Fatalf("stored=%d split=%+v legacy=%+v", stored, got, want)
		}
	}
}

// TestPromptEstimate_NilSafe keeps nil requests and stores harmless.
func TestPromptEstimate_NilSafe(t *testing.T) {
	ctx := context.Background()
	var nilEst *PromptEstimate
	if nilEst.InputTokens() != 0 || nilEst.Request() != nil || nilEst.prefixBlocks() != nil {
		t.Fatal("nil PromptEstimate must be zero")
	}
	est := PreparePromptEstimate(nil)
	if est.InputTokens() != 0 || est.prefixBlocks() != nil {
		t.Fatal("nil request must be zero")
	}
	if got := est.CacheUsageSplit(ctx, NewMemoryCacheFingerprintStore(), "k"); got != (CacheUsageSplit{}) {
		t.Fatalf("nil request split=%+v", got)
	}
	est.CommitCacheFingerprints(ctx, NewMemoryCacheFingerprintStore(), "k", 0)
}

// BenchmarkKiroPromptUsageFlowPrepared is the post-change per-request sequence
// (one PromptEstimate shared by input total, split and commit). Compare with
// BenchmarkKiroPromptUsageFlowLegacy.
func BenchmarkKiroPromptUsageFlowPrepared(b *testing.B) {
	req := realisticKiroRequest(24)
	store, session := benchSeededStore(b, req)
	b.SetBytes(int64(benchRequestBytes(req)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		est := PreparePromptEstimate(req)
		_ = est.InputTokens()
		_ = est.CacheUsageSplit(context.Background(), store, session)
		est.CommitCacheFingerprints(context.Background(), store, session, DefaultCacheTTL)
	}
}

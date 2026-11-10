package kiro

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// PromptEstimate memoizes the prompt-side token work for one Kiro request.
//
// A single request used to run EstimateInputTokens twice (kiroPromptUsage and
// again inside EstimateCacheUsageSplit) and built cache-prefix blocks twice
// (split, then CommitCacheFingerprints), so every prompt byte went through the
// cl100k tokenizer four times and every tool schema / tool_use input was JSON
// marshaled four times. PromptEstimate flattens the request once and computes
// each tokenized string exactly once:
//
//   - the input total (system + message parts joined, plus the tools string),
//   - the labeled cache-prefix segments (one Count per segment).
//
// The two families tokenize different strings (segment labels, empty
// name/description handling), and cl100k counts are not additive across
// concatenation, so neither is derived from the other; the numbers are
// byte-for-byte what the previous per-call implementation produced.
//
// The request must not be mutated after PreparePromptEstimate. Not safe for
// concurrent use; it is owned by one forward call.
type PromptEstimate struct {
	req *ClaudeRequest

	flattened    bool
	systemText   string
	messageParts [][]string // appendMessageContentForEstimate(nil, msg.Content) per message
	toolSchemas  []string   // marshalJSONForEstimate(tool.InputSchema) per tool

	inputDone   bool
	inputTokens int

	blocksDone bool
	blocks     []cachePrefixBlock
}

// PreparePromptEstimate binds req for memoized estimation. Work is lazy.
func PreparePromptEstimate(req *ClaudeRequest) *PromptEstimate {
	return &PromptEstimate{req: req}
}

// Request returns the bound request (may be nil).
func (p *PromptEstimate) Request() *ClaudeRequest {
	if p == nil {
		return nil
	}
	return p.req
}

func (p *PromptEstimate) flatten() {
	if p.flattened {
		return
	}
	p.flattened = true
	req := p.req
	p.systemText = extractSystemPrompt(req.System)
	p.messageParts = make([][]string, len(req.Messages))
	for i := range req.Messages {
		p.messageParts[i] = appendMessageContentForEstimate(nil, req.Messages[i].Content)
	}
	p.toolSchemas = make([]string, len(req.Tools))
	for i := range req.Tools {
		p.toolSchemas[i] = marshalJSONForEstimate(req.Tools[i].InputSchema)
	}
}

// InputTokens is the EstimateInputTokens value for the bound request.
func (p *PromptEstimate) InputTokens() int {
	if p == nil || p.req == nil {
		return 0
	}
	if p.inputDone {
		return p.inputTokens
	}
	p.flatten()

	n := 0
	if p.systemText != "" {
		n++
	}
	for _, mp := range p.messageParts {
		n += len(mp)
	}
	parts := make([]string, 0, n)
	if p.systemText != "" {
		parts = append(parts, p.systemText)
	}
	for _, mp := range p.messageParts {
		parts = append(parts, mp...)
	}
	total := countTokens(strings.Join(parts, "\n"))

	// Tools definitions: name + description + serialized input_schema, empty
	// fields skipped.
	if tools := p.req.Tools; len(tools) > 0 {
		toolParts := make([]string, 0, len(tools)*3)
		for i := range tools {
			if tools[i].Name != "" {
				toolParts = append(toolParts, tools[i].Name)
			}
			if tools[i].Description != "" {
				toolParts = append(toolParts, tools[i].Description)
			}
			if js := p.toolSchemas[i]; js != "" {
				toolParts = append(toolParts, js)
			}
		}
		total += countTokens(strings.Join(toolParts, "\n"))
	}

	p.inputTokens = total
	p.inputDone = true
	return total
}

// prefixBlocks returns the chained prefix fingerprints with cumulative segment
// token counts, computed once per request.
func (p *PromptEstimate) prefixBlocks() []cachePrefixBlock {
	if p == nil || p.req == nil {
		return nil
	}
	if p.blocksDone {
		return p.blocks
	}
	p.blocksDone = true
	p.flatten()
	req := p.req

	segments := make([]string, 0, 2+len(req.Messages))
	if p.systemText != "" {
		segments = append(segments, "system\n"+p.systemText)
	}
	if tools := req.Tools; len(tools) > 0 {
		// Cache segment keeps empty name/description entries (unlike InputTokens).
		toolParts := make([]string, 0, len(tools)*3)
		for i := range tools {
			toolParts = append(toolParts, tools[i].Name, tools[i].Description)
			if js := p.toolSchemas[i]; js != "" {
				toolParts = append(toolParts, js)
			}
		}
		if s := strings.Join(toolParts, "\n"); s != "" {
			segments = append(segments, "tools\n"+s)
		}
	}
	for i := range req.Messages {
		segments = append(segments,
			"msg:"+req.Messages[i].Role+":"+strconv.Itoa(i)+"\n"+strings.Join(p.messageParts[i], "\n"))
	}
	if len(segments) == 0 {
		return nil
	}

	running := sha256.Sum256([]byte("kiro-cache-v1|" + strings.TrimSpace(req.Model)))
	var chain [2 * sha256.Size]byte
	cumTokens := 0
	out := make([]cachePrefixBlock, 0, len(segments))
	for _, seg := range segments {
		// running = sha256(running || sha256(seg))
		segHash := sha256.Sum256([]byte(seg))
		copy(chain[:sha256.Size], running[:])
		copy(chain[sha256.Size:], segHash[:])
		running = sha256.Sum256(chain[:])
		cumTokens += countTokens(seg)
		out = append(out, cachePrefixBlock{
			fingerprintHex: hex.EncodeToString(running[:]),
			cumTokens:      cumTokens,
		})
	}
	p.blocks = out
	return out
}

// CacheUsageSplit is EstimateCacheUsageSplit for the bound request, reusing the
// memoized input total and prefix blocks.
func (p *PromptEstimate) CacheUsageSplit(ctx context.Context, store CacheFingerprintStore, sessionKey string) CacheUsageSplit {
	total := p.InputTokens()
	split := CacheUsageSplit{InputTokens: total}
	if total <= 0 || store == nil || strings.TrimSpace(sessionKey) == "" || p.Request() == nil {
		return split
	}

	blocks := p.prefixBlocks()
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
		// Align fingerprint-segment counts with InputTokens (labels differ).
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

// CommitCacheFingerprints is the package-level CommitCacheFingerprints for the
// bound request, reusing prefix blocks already built by CacheUsageSplit.
func (p *PromptEstimate) CommitCacheFingerprints(ctx context.Context, store CacheFingerprintStore, sessionKey string, ttl time.Duration) {
	if store == nil || strings.TrimSpace(sessionKey) == "" || p.Request() == nil {
		return
	}
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if ttl > MaxCacheTTL {
		ttl = MaxCacheTTL
	}
	for _, b := range p.prefixBlocks() {
		if b.cumTokens <= 0 {
			continue
		}
		_ = store.Put(ctx, sessionKey, b.fingerprintHex, b.cumTokens, ttl)
	}
}

package kiro

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

// Token estimation for the Kiro (sixth platform) CodeWhisperer upstream.
//
// Kiro's EventStream upstream returns *credits only* — it never reports
// input/output token counts. Without estimation every Kiro request bills as
// input=output=cost=0 (100% free). This package estimates token usage locally
// with the cl100k_base tokenizer so the downstream billing pipeline
// (RecordUsage → CalculateCostUnified) can charge against the mapped claude-*
// price table exactly as the native Anthropic platform does.
//
// Calibration choices (approved):
//   - cl100k_base encoding (close enough to Anthropic's tokenizer for billing).
//   - No conservative multiplier: cl100k naturally over-counts CJK text, which
//     is the operator-safe direction.
//   - Kiro has no cache semantics: cache token fields stay 0, never fabricated.
//   - Estimates are tagged with billing_tier="kiro-estimated" by the caller.

// KiroEstimatedBillingTier is the billing_tier label stamped on Kiro usage logs
// so operators can distinguish estimated billing from upstream-reported billing.
const KiroEstimatedBillingTier = "kiro-estimated"

var (
	estCodec     tokenizer.Codec
	estCodecOnce sync.Once
)

// codec lazily initializes and caches the cl100k_base tokenizer. A nil return
// means initialization failed; callers fall back to the rune heuristic.
func codec() tokenizer.Codec {
	estCodecOnce.Do(func() {
		if c, err := tokenizer.Get(tokenizer.Cl100kBase); err == nil {
			estCodec = c
		}
	})
	return estCodec
}

// countTokens returns an estimated token count for s. It uses cl100k_base when
// available and falls back to a len([]rune)/4 heuristic on any encode failure.
// It never panics and never returns 0 for non-empty input (the fallback floors
// at 1 token for any non-empty string).
func countTokens(s string) int {
	if s == "" {
		return 0
	}
	if c := codec(); c != nil {
		if n, err := c.Count(s); err == nil {
			return n
		}
	}
	return fallbackCount(s)
}

// fallbackCount approximates token count as ~4 chars/token, flooring at 1 for
// any non-empty string so we never silently bill an interaction as free.
func fallbackCount(s string) int {
	if s == "" {
		return 0
	}
	n := len([]rune(s)) / 4
	if n < 1 {
		n = 1
	}
	return n
}

// EstimateInputTokens estimates the prompt-side token count for a Kiro request.
//
// Counted (mirrors what is actually sent upstream):
//   - system prompt (string or []SystemBlock, flattened),
//   - every message's text content,
//   - tool_result text (the tool output echoed back into the prompt),
//   - tool_use Input JSON (assistant tool calls in history),
//   - tools definitions: name + description + serialized input_schema.
//
// Skipped: image blocks (binary, not text-tokenized).
func EstimateInputTokens(req *ClaudeRequest) int {
	if req == nil {
		return 0
	}

	parts := make([]string, 0, 8)

	// System prompt (raw flatten — same shape extractSystemPrompt produces).
	if sys := extractSystemPrompt(req.System); sys != "" {
		parts = append(parts, sys)
	}

	// Messages: text + tool_result text + tool_use Input JSON.
	for i := range req.Messages {
		parts = appendMessageContentForEstimate(parts, req.Messages[i].Content)
	}

	total := countTokens(strings.Join(parts, "\n"))

	// Tools definitions count as input (the model sees them in the prompt).
	total += estimateToolsTokens(req.Tools)

	return total
}

// appendMessageContentForEstimate flattens one message's content into parts for
// estimation. It handles the string form and the []ContentBlock form, pulling
// text, tool_result text, and tool_use Input JSON; image blocks are skipped.
func appendMessageContentForEstimate(parts []string, content any) []string {
	if s, ok := content.(string); ok {
		if s != "" {
			parts = append(parts, s)
		}
		return parts
	}

	blocks, ok := content.([]any)
	if !ok {
		return parts
	}
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch blockType, _ := block["type"].(string); blockType {
		case "text", "input_text":
			if t, ok := block["text"].(string); ok && t != "" {
				parts = append(parts, t)
			}
		case "tool_result":
			// tool_result content is echoed back into the prompt; count its text.
			if txt, _ := extractToolResultContent(block["content"]); txt != "" {
				parts = append(parts, txt)
			}
		case "tool_use":
			// Assistant tool call sitting in history: count its serialized input.
			if js := marshalJSONForEstimate(block["input"]); js != "" {
				parts = append(parts, js)
			}
			if name, ok := block["name"].(string); ok && name != "" {
				parts = append(parts, name)
			}
		case "image", "image_url", "input_image":
			// Skipped: binary, not text-tokenized.
		}
	}
	return parts
}

// estimateToolsTokens counts the tokens contributed by tool definitions:
// name + description + the serialized input_schema for each tool.
func estimateToolsTokens(tools []ClaudeTool) int {
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

// EstimateOutputTokens estimates the completion-side token count for a Kiro
// response: assistant text + thinking/reasoning text + serialized tool_use
// blocks (id + name + input). All three are billed as output.
func EstimateOutputTokens(text, thinking string, toolUses []KiroToolUse) int {
	parts := make([]string, 0, 2+len(toolUses)*3)
	if text != "" {
		parts = append(parts, text)
	}
	if thinking != "" {
		parts = append(parts, thinking)
	}
	for i := range toolUses {
		tu := toolUses[i]
		if tu.Name != "" {
			parts = append(parts, tu.Name)
		}
		if tu.ToolUseID != "" {
			parts = append(parts, tu.ToolUseID)
		}
		if js := marshalJSONForEstimate(tu.Input); js != "" {
			parts = append(parts, js)
		}
	}
	return countTokens(strings.Join(parts, "\n"))
}

// marshalJSONForEstimate serializes v to compact JSON for token estimation.
// Nil / empty maps and marshal failures yield an empty string (no tokens).
func marshalJSONForEstimate(v any) string {
	if v == nil {
		return ""
	}
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return ""
	}
	js, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := string(js)
	if s == "null" || s == "{}" || s == "[]" {
		return ""
	}
	return s
}

//go:build unit

package kiro

import "testing"

// TestCountTokens_NonEmptyNeverZero asserts that the tokenizer path returns a
// positive, reasonable count for ordinary text and never zero for non-empty
// input.
func TestCountTokens_NonEmptyNeverZero(t *testing.T) {
	if got := countTokens(""); got != 0 {
		t.Fatalf("countTokens(empty) = %d, want 0", got)
	}

	// "hello world" tokenizes to 2 tokens under cl100k_base; assert non-zero and
	// in a sane band rather than an exact value (tokenizer impl may shift).
	got := countTokens("hello world")
	if got <= 0 {
		t.Fatalf("countTokens(hello world) = %d, want > 0", got)
	}
	if got > 10 {
		t.Fatalf("countTokens(hello world) = %d, unexpectedly large", got)
	}
}

// TestFallbackCount_FloorsAtOne asserts the rune-heuristic fallback never
// returns 0 for non-empty input (which would silently bill an interaction as
// free), and approximates ~4 chars/token for longer strings.
func TestFallbackCount_FloorsAtOne(t *testing.T) {
	if got := fallbackCount(""); got != 0 {
		t.Fatalf("fallbackCount(empty) = %d, want 0", got)
	}
	if got := fallbackCount("a"); got != 1 {
		t.Fatalf("fallbackCount(short) = %d, want 1 (floor)", got)
	}
	// 40 ASCII chars → ~10 tokens.
	long := ""
	for i := 0; i < 40; i++ {
		long += "x"
	}
	if got := fallbackCount(long); got != 10 {
		t.Fatalf("fallbackCount(40 chars) = %d, want 10", got)
	}
}

// TestEstimateInputTokens_CountsSystemMessagesToolsAndToolUse asserts the input
// estimate is non-zero and grows with system prompt, message text, tool
// definitions, tool_use input JSON, and tool_result text.
func TestEstimateInputTokens_CountsSystemMessagesToolsAndToolUse(t *testing.T) {
	if got := EstimateInputTokens(nil); got != 0 {
		t.Fatalf("EstimateInputTokens(nil) = %d, want 0", got)
	}

	// Baseline: a single short user message.
	base := &ClaudeRequest{
		Model: "claude-sonnet-4-5",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "What is the weather in Paris today?"},
		},
	}
	baseTok := EstimateInputTokens(base)
	if baseTok <= 0 {
		t.Fatalf("baseline input tokens = %d, want > 0", baseTok)
	}

	// Adding a system prompt must increase the estimate.
	withSystem := &ClaudeRequest{
		Model:    "claude-sonnet-4-5",
		System:   "You are a meticulous and concise travel assistant. Always answer briefly.",
		Messages: base.Messages,
	}
	if got := EstimateInputTokens(withSystem); got <= baseTok {
		t.Fatalf("with system = %d, want > baseline %d", got, baseTok)
	}

	// Adding tools (name + description + input_schema) must increase the estimate.
	withTools := &ClaudeRequest{
		Model:    "claude-sonnet-4-5",
		Messages: base.Messages,
		Tools: []ClaudeTool{
			{
				Name:        "get_weather",
				Description: "Fetch the current weather for a given city and country.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city":    map[string]any{"type": "string"},
						"country": map[string]any{"type": "string"},
					},
					"required": []any{"city"},
				},
			},
		},
	}
	if got := EstimateInputTokens(withTools); got <= baseTok {
		t.Fatalf("with tools = %d, want > baseline %d", got, baseTok)
	}

	// Block-shaped content with tool_use input JSON and tool_result text.
	withBlocks := &ClaudeRequest{
		Model: "claude-sonnet-4-5",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "Look up the weather please."},
			{Role: "assistant", Content: []any{
				map[string]any{
					"type": "tool_use",
					"id":   "toolu_1",
					"name": "get_weather",
					"input": map[string]any{
						"city":    "Paris",
						"country": "France",
					},
				},
			}},
			{Role: "user", Content: []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_1",
					"content":     "It is 21 degrees and sunny in Paris.",
				},
			}},
		},
	}
	if got := EstimateInputTokens(withBlocks); got <= 0 {
		t.Fatalf("block-shaped input tokens = %d, want > 0", got)
	}
}

// TestEstimateInputTokens_SkipsImages asserts image blocks do not contribute
// tokens (they are binary and not text-tokenized).
func TestEstimateInputTokens_SkipsImages(t *testing.T) {
	textOnly := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": "describe this"},
			}},
		},
	}
	withImage := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": "describe this"},
				map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": "image/png",
						"data":       "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
					},
				},
			}},
		},
	}
	if EstimateInputTokens(withImage) != EstimateInputTokens(textOnly) {
		t.Fatalf("image block changed input estimate; images must be skipped")
	}
}

// TestEstimateOutputTokens_CountsTextThinkingAndToolUse asserts the output
// estimate sums assistant text, thinking, and serialized tool_use blocks.
func TestEstimateOutputTokens_CountsTextThinkingAndToolUse(t *testing.T) {
	if got := EstimateOutputTokens("", "", nil); got != 0 {
		t.Fatalf("EstimateOutputTokens(empty) = %d, want 0", got)
	}

	textOnly := EstimateOutputTokens("The weather in Paris is sunny.", "", nil)
	if textOnly <= 0 {
		t.Fatalf("text-only output tokens = %d, want > 0", textOnly)
	}

	withThinking := EstimateOutputTokens(
		"The weather in Paris is sunny.",
		"Let me reason about the user's request before answering.",
		nil,
	)
	if withThinking <= textOnly {
		t.Fatalf("with thinking = %d, want > text-only %d", withThinking, textOnly)
	}

	withTools := EstimateOutputTokens(
		"",
		"",
		[]KiroToolUse{
			{
				ToolUseID: "toolu_99",
				Name:      "get_weather",
				Input:     map[string]any{"city": "Paris", "country": "France"},
			},
		},
	)
	if withTools <= 0 {
		t.Fatalf("tool_use-only output tokens = %d, want > 0", withTools)
	}
}

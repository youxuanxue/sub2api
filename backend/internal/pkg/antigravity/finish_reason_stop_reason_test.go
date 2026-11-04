//go:build unit

package antigravity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamingProcessor_ContentFilterStopReason(t *testing.T) {
	tests := []struct {
		name         string
		finishReason string
		tool         bool
		want         string
	}{
		{name: "SAFETY", finishReason: "SAFETY", want: "refusal"},
		{name: "STOP", finishReason: "STOP", want: "end_turn"},
		{name: "MAX_TOKENS", finishReason: "MAX_TOKENS", want: "max_tokens"},
		{name: "SAFETY with tool call", finishReason: "SAFETY", tool: true, want: "tool_use"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewStreamingProcessor("gemini-test")
			out := p.ProcessLine(`data: {"response":` + geminiFinishLine(t, tt.finishReason, tt.tool) + `}`)
			reasons := messageDeltaStopReasons(t, out)
			require.Equal(t, []string{tt.want}, reasons)
			if tt.want == "refusal" {
				require.NotContains(t, string(out), `"stop_reason":"end_turn"`)
			}
		})
	}
}

func TestNonStreamingProcessor_ContentFilterStopReason(t *testing.T) {
	tests := []struct {
		name         string
		finishReason string
		want         string
	}{
		{name: "SAFETY", finishReason: "SAFETY", want: "refusal"},
		{name: "STOP", finishReason: "STOP", want: "end_turn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _, err := TransformGeminiToClaude([]byte(geminiFinishLine(t, tt.finishReason, false)), "gemini-test")
			require.NoError(t, err)
			var resp ClaudeResponse
			require.NoError(t, json.Unmarshal(raw, &resp))
			require.Equal(t, tt.want, resp.StopReason)
		})
	}
}

func geminiFinishLine(t *testing.T, finishReason string, tool bool) string {
	t.Helper()
	var part map[string]any
	if tool {
		part = map[string]any{"functionCall": map[string]any{"name": "read", "args": map[string]any{}}}
	} else {
		part = map[string]any{"text": "hello"}
	}
	body := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"role":  "model",
					"parts": []any{part},
				},
				"finishReason": finishReason,
			},
		},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return string(raw)
}

func messageDeltaStopReasons(t *testing.T, raw []byte) []string {
	t.Helper()
	var reasons []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("decode sse data: %v\n%s", err, payload)
		}
		if event.Type == "message_delta" {
			reasons = append(reasons, event.Delta.StopReason)
		}
	}
	return reasons
}

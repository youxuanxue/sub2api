//go:build unit

package antigravity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamingProcessor_TerminalFault(t *testing.T) {
	t.Run("no message_start does not invent a finish", func(t *testing.T) {
		p := NewStreamingProcessor("gemini-test")
		out, usage := p.Finish()
		require.Empty(t, out)
		require.NotNil(t, usage)
		require.Equal(t, StreamTerminalNone, p.TerminalFault())
		require.False(t, p.HasVisibleOutput())
	})

	t.Run("thinking without finishReason does not synthesize end_turn", func(t *testing.T) {
		p := NewStreamingProcessor("gemini-test")
		line := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"plan","thought":true}]}}],"usageMetadata":{"promptTokenCount":5,"thoughtsTokenCount":3}}}`
		streamed := p.ProcessLine(line)
		finished, usage := p.Finish()
		combined := string(streamed) + string(finished)
		require.Contains(t, string(streamed), "message_start")
		require.Contains(t, string(streamed), "plan")
		require.NotContains(t, string(finished), "end_turn")
		require.NotContains(t, combined, "end_turn")
		require.NotContains(t, combined, "message_delta")
		require.Equal(t, StreamTerminalMissing, p.TerminalFault())
		require.False(t, p.HasVisibleOutput())
		require.Equal(t, 5, usage.InputTokens)
		require.Equal(t, 3, usage.OutputTokens)
	})

	t.Run("MALFORMED_FUNCTION_CALL does not synthesize end_turn", func(t *testing.T) {
		p := NewStreamingProcessor("gemini-test")
		line := `data: {"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","content":{"parts":[{"functionCall":{"name":"read","args":{}}}]}}]}}`
		streamed := p.ProcessLine(line)
		finished, _ := p.Finish()
		combined := string(streamed) + string(finished)
		require.Contains(t, combined, "tool_use")
		require.NotContains(t, string(finished), "end_turn")
		require.NotContains(t, combined, "end_turn")
		require.NotContains(t, combined, `"stop_reason":"tool_use"`)
		require.Equal(t, StreamTerminalMalformed, p.TerminalFault())
		require.True(t, p.HasVisibleOutput())
	})

	t.Run("real finish reasons keep their stop_reason", func(t *testing.T) {
		cases := []struct {
			name         string
			finishReason string
			tool         bool
			want         string
		}{
			{name: "STOP", finishReason: "STOP", want: "end_turn"},
			{name: "tool_use", finishReason: "STOP", tool: true, want: "tool_use"},
			{name: "MAX_TOKENS", finishReason: "MAX_TOKENS", want: "max_tokens"},
			{name: "content filter", finishReason: "SAFETY", want: "refusal"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				p := NewStreamingProcessor("gemini-test")
				streamed := p.ProcessLine(`data: {"response":` + geminiFinishLine(t, tc.finishReason, tc.tool) + `}`)
				finished, usage := p.Finish()
				require.NotContains(t, string(finished), "stop_reason")
				require.Equal(t, []string{tc.want}, messageDeltaStopReasons(t, streamed))
				require.Equal(t, StreamTerminalNone, p.TerminalFault())
				require.NotNil(t, usage)
			})
		}
	})
}

func TestStreamingProcessor_TerminalFaultMalformedWithoutParts(t *testing.T) {
	p := NewStreamingProcessor("gemini-test")
	streamed := p.ProcessLine(`data: {"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`)
	finished, _ := p.Finish()
	combined := string(streamed) + string(finished)
	require.Contains(t, combined, "message_start")
	require.NotContains(t, combined, "end_turn")
	require.Equal(t, StreamTerminalMalformed, p.TerminalFault())
	require.False(t, strings.Contains(combined, `"stop_reason":"end_turn"`))
}

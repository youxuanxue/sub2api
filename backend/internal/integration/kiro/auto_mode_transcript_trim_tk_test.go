package kiro

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectClaudeAutoModeTranscriptCandidate(t *testing.T) {
	t.Parallel()
	ok := &ClaudeRequest{
		MaxTokens: 64,
		Messages: []ClaudeMessage{{
			Role:    "user",
			Content: "<transcript>\n{\"role\":\"user\",\"type\":\"user\"}\n</transcript>",
		}},
	}
	require.True(t, DetectClaudeAutoModeTranscriptCandidate(ok))

	require.False(t, DetectClaudeAutoModeTranscriptCandidate(&ClaudeRequest{
		MaxTokens: 1024,
		Messages:  ok.Messages,
	}), "large max_tokens must not match classifier budget")

	require.False(t, DetectClaudeAutoModeTranscriptCandidate(&ClaudeRequest{
		MaxTokens: 64,
		Tools:     []ClaudeTool{{Name: "Bash"}},
		Messages:  ok.Messages,
	}), "tool schemas are outside classifier shape")

	require.False(t, DetectClaudeAutoModeTranscriptCandidate(&ClaudeRequest{
		MaxTokens: 64,
		Messages:  []ClaudeMessage{{Role: "user", Content: "plain oversized prose"}},
	}))
}

func TestMaybeTrimClaudeAutoModeTranscript_RemovesOldToolProcessKeepsUserAndAction(t *testing.T) {
	t.Parallel()
	interior := strings.Join([]string{
		`{"role":"user","type":"user","text":"do not deploy to prod"}`,
		`{"type":"tool_result","name":"bash","text":"old ls output"}`,
		`{"type":"tool_use","name":"bash","text":"old command"}`,
		`{"type":"meta","text":"status ok"}`,
		`{"type":"action","name":"bash","text":"rm -rf /"}`,
	}, "\n")
	req := &ClaudeRequest{
		MaxTokens: 64,
		Messages: []ClaudeMessage{
			{Role: "user", Content: "preamble"},
			{Role: "user", Content: autoModeTranscriptOpen + "\n" + interior + "\n" + autoModeTranscriptClose},
		},
	}
	rep := MaybeTrimClaudeAutoModeTranscript(req)
	require.True(t, rep.Candidate)
	require.True(t, rep.Applied, "skip=%s", rep.SkipReason)
	require.Equal(t, "ndjson", rep.Shape)
	require.Greater(t, rep.ToolProcessRemoved, 0)
	joined := joinClaudeUserText(req)
	require.Contains(t, joined, `do not deploy to prod`)
	require.Contains(t, joined, `"type":"action"`)
	require.Contains(t, joined, autoModeTrimPlaceholder)
	require.NotContains(t, joined, "old ls output")
	require.Equal(t, "preamble", claudeMessagePlainText(req.Messages[0].Content)[0],
		"non-transcript user turn must stay intact")
}

func TestTrimClaudeAutoModeTranscriptUntil_StopsWhenBudgetFits(t *testing.T) {
	t.Parallel()
	interior := strings.Join([]string{
		`{"role":"user","type":"user","text":"keep me"}`,
		`{"type":"tool_result","name":"bash","text":"drop-1"}`,
		`{"type":"tool_result","name":"bash","text":"drop-2"}`,
		`{"type":"action","name":"bash","text":"latest"}`,
	}, "\n")
	req := &ClaudeRequest{
		MaxTokens: 64,
		Messages: []ClaudeMessage{{
			Role:    "user",
			Content: autoModeTranscriptOpen + "\n" + interior + "\n" + autoModeTranscriptClose,
		}},
	}
	rep := TrimClaudeAutoModeTranscriptUntil(req, func(r *ClaudeRequest) bool {
		// Still "over" while the oldest tool-process record remains.
		return strings.Contains(joinClaudeUserText(r), "drop-1")
	})
	require.True(t, rep.Applied, "skip=%s", rep.SkipReason)
	require.Equal(t, 1, rep.ToolProcessRemoved)
	require.Equal(t, 1, rep.Rounds)
	joined := joinClaudeUserText(req)
	require.NotContains(t, joined, "drop-1")
	require.Contains(t, joined, "drop-2", "second tool-process kept once budget fits")
	require.Contains(t, joined, "keep me")
	require.Contains(t, joined, "latest")
}

func TestTrimClaudeAutoModeTranscript_ContentBlocksShape(t *testing.T) {
	t.Parallel()
	blocks := []ClaudeContentBlock{
		{Type: "text", Text: "prefix turn"},
		{Type: "text", Text: autoModeTranscriptOpen},
		{Type: "text", Text: `{"role":"user","type":"user","text":"deny prod"}`},
		{Type: "text", Text: `{"type":"tool_result","name":"read","text":"old file"}`},
		{Type: "text", Text: `{"type":"action","name":"bash","text":"echo hi"}`},
		{Type: "text", Text: autoModeTranscriptClose},
	}
	req := &ClaudeRequest{
		MaxTokens: 64,
		Messages:  []ClaudeMessage{{Role: "user", Content: blocks}},
	}
	rep := MaybeTrimClaudeAutoModeTranscript(req)
	require.True(t, rep.Applied, "skip=%s shape=%s", rep.SkipReason, rep.Shape)
	require.Equal(t, "content_blocks", rep.Shape)
	require.Equal(t, 1, rep.ToolProcessRemoved)
	out, ok := req.Messages[0].Content.([]ClaudeContentBlock)
	require.True(t, ok)
	joined := joinClaudeUserText(req)
	require.Contains(t, joined, "deny prod")
	require.Contains(t, joined, "echo hi")
	require.NotContains(t, joined, "old file")
	require.Equal(t, "prefix turn", out[0].Text)
}

func TestMaybeTrimClaudeAutoModeTranscript_ParseFailureLeavesRequestUntouched(t *testing.T) {
	t.Parallel()
	raw := autoModeTranscriptOpen + "\nnot-json\n" + autoModeTranscriptClose
	req := &ClaudeRequest{
		MaxTokens: 64,
		Messages:  []ClaudeMessage{{Role: "user", Content: raw}},
	}
	before := joinClaudeUserText(req)
	rep := MaybeTrimClaudeAutoModeTranscript(req)
	require.True(t, rep.Candidate)
	require.False(t, rep.Applied)
	require.Equal(t, "parse_failed", rep.SkipReason)
	require.Equal(t, before, joinClaudeUserText(req))
}

func TestMaybeTrimClaudeAutoModeTranscript_OrdinaryUserTextNeverCandidate(t *testing.T) {
	t.Parallel()
	req := &ClaudeRequest{
		MaxTokens: 4096,
		Messages:  []ClaudeMessage{{Role: "user", Content: strings.Repeat("x", 2000)}},
	}
	rep := MaybeTrimClaudeAutoModeTranscript(req)
	require.False(t, rep.Applied)
	require.Equal(t, "not_candidate", rep.SkipReason)
}

func TestTrimClaudeAutoModeTranscriptUntil_AlreadyFitsNoMutation(t *testing.T) {
	t.Parallel()
	req := &ClaudeRequest{
		MaxTokens: 64,
		Messages: []ClaudeMessage{{
			Role: "user",
			Content: autoModeTranscriptOpen + "\n" +
				`{"role":"user","type":"user","text":"x"}` + "\n" +
				`{"type":"tool_result","name":"bash","text":"y"}` + "\n" +
				`{"type":"action","name":"bash","text":"z"}` + "\n" +
				autoModeTranscriptClose,
		}},
	}
	before := joinClaudeUserText(req)
	rep := TrimClaudeAutoModeTranscriptUntil(req, func(*ClaudeRequest) bool { return false })
	require.True(t, rep.Candidate)
	require.False(t, rep.Applied)
	require.Equal(t, "already_fits", rep.SkipReason)
	require.Equal(t, before, joinClaudeUserText(req))
}

//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests for upstream Wei-Shaw/sub2api#1946 — Anthropic monitor text extraction
// must not assume content[0] is the text block. With extended thinking the
// first block is a thinking block, so the legacy "content.0.text" path yields
// "" and a healthy account is wrongly marked as a challenge mismatch.
func TestExtractAnthropicText(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "plain text first block (regression baseline)",
			body: `{"content":[{"type":"text","text":"4"}]}`,
			want: "4",
		},
		{
			name: "thinking block before text block (#1946)",
			body: `{"content":[{"type":"thinking","thinking":"the user asks 2+2"},{"type":"text","text":"4"}]}`,
			want: "4",
		},
		{
			name: "redacted_thinking before text",
			body: `{"content":[{"type":"redacted_thinking","data":"xx"},{"type":"text","text":"42"}]}`,
			want: "42",
		},
		{
			name: "multiple text blocks concatenated",
			body: `{"content":[{"type":"thinking","thinking":"…"},{"type":"text","text":"4"},{"type":"text","text":"2"}]}`,
			want: "42",
		},
		{
			name: "no text block falls back to empty",
			body: `{"content":[{"type":"thinking","thinking":"…"}]}`,
			want: "",
		},
		{
			name: "missing type defaults to text (legacy shape)",
			body: `{"content":[{"text":"7"}]}`,
			want: "7",
		},
		{
			name: "non-array content falls back to legacy gjson path",
			body: `{"content":"plain string"}`,
			want: "", // content.0.text on a string yields ""
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractAnthropicText([]byte(tc.body)); got != tc.want {
				t.Fatalf("extractAnthropicText = %q, want %q", got, tc.want)
			}
		})
	}
}

// anthropicThinkingFirstHandler answers the arithmetic challenge but places a
// thinking block BEFORE the text block — the exact shape that broke the legacy
// "content.0.text" extraction.
type anthropicThinkingFirstHandler struct{}

func (anthropicThinkingFirstHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	var parsed map[string]any
	_ = json.NewDecoder(r.Body).Decode(&parsed)
	prompt := ""
	if msgs, ok := parsed["messages"].([]any); ok && len(msgs) > 0 {
		if m, ok := msgs[0].(map[string]any); ok {
			prompt, _ = m["content"].(string)
		}
	}
	answer := answerFromChallengePrompt(prompt)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"content": []map[string]any{
			{"type": "thinking", "thinking": "let me compute the sum"},
			{"type": "text", "text": answer},
		},
	})
}

// End-to-end coverage of the callProvider → extractAnthropicText dispatch wiring.
// Without the Anthropic dispatch branch the monitor reads the leading thinking
// block (no .text → "") and fails the challenge, so this test fails if the hook
// is ever removed.
func TestRunCheckForModel_Anthropic_ThinkingFirstStillOperational(t *testing.T) {
	swapMonitorHTTPClient(t)
	srv := httptest.NewServer(anthropicThinkingFirstHandler{})
	t.Cleanup(srv.Close)

	res := runCheckForModel(context.Background(), MonitorProviderAnthropic, srv.URL, "sk-fake", "claude-x", nil)

	require.Equal(t, MonitorStatusOperational, res.Status,
		"#1946: a thinking-first Anthropic response with the correct answer must be operational; got message=%q", res.Message)
}

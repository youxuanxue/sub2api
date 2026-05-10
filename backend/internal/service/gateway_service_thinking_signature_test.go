//go:build unit

package service

import (
	"testing"
)

func TestIsThinkingBlockSignatureError_MustContainThinking(t *testing.T) {
	svc := &GatewayService{}

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "anthropic empty thinking field — exact prod incident message",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"messages.11.content.0.thinking: each thinking block must contain thinking"},"request_id":"req_011Cat8muQgKEVtqTzncEjfS"}`,
			want: true,
		},
		{
			name: "alternate phrasing thinking block must contain",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"messages.0.content.0: thinking block must contain thinking"}}`,
			want: true,
		},
		{
			name: "case insensitive",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"Each Thinking Block Must Contain Thinking"}}`,
			want: true,
		},
		{
			name: "existing signature pattern still matches",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`,
			want: true,
		},
		{
			name: "existing expected pattern still matches",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"Expected thinking or redacted_thinking but found text"}}`,
			want: true,
		},
		{
			name: "existing cannot be modified pattern still matches",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"thinking blocks in the latest assistant message cannot be modified"}}`,
			want: true,
		},
		{
			name: "existing empty content pattern still matches",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"all messages must have non-empty content"}}`,
			want: true,
		},
		{
			name: "unrelated 400 must not trigger rectifier",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens exceeded model context window"}}`,
			want: false,
		},
		{
			name: "empty body returns false",
			body: ``,
			want: false,
		},
		{
			name: "non-json body without thinking keyword returns false",
			body: `internal server error`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := svc.isThinkingBlockSignatureError([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("isThinkingBlockSignatureError(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

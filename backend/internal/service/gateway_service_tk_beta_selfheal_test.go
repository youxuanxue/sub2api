//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRejectedAnthropicBetas(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "single rejected token (claude-code#53855)",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"Unexpected value(s) ` + "`effort-2025-11-24`" + ` for the ` + "`anthropic-beta`" + ` header. Please consult our documentation at docs.anthropic.com or try again without the header."}}`,
			want: []string{"effort-2025-11-24"},
		},
		{
			name: "multiple rejected tokens",
			body: "Unexpected value(s) `foo-2025-01-01`, `bar-2025-02-02` for the `anthropic-beta` header.",
			want: []string{"foo-2025-01-01", "bar-2025-02-02"},
		},
		{
			name: "header name anthropic-beta is not harvested as a value",
			body: "Unexpected value(s) `only-this-2026-01-01` for the `anthropic-beta` header.",
			want: []string{"only-this-2026-01-01"},
		},
		{
			name: "unrelated 400 is not a beta rejection",
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: must be greater than thinking budget_tokens"}}`,
			want: nil,
		},
		{
			name: "empty body",
			body: "",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRejectedAnthropicBetas([]byte(tc.body))
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBetaSelfHealDropContextRoundTrip(t *testing.T) {
	require.Nil(t, betaSelfHealDropTokens(context.Background()))
	ctx := withBetaSelfHealDrop(context.Background(), []string{"effort-2025-11-24"})
	require.Equal(t, []string{"effort-2025-11-24"}, betaSelfHealDropTokens(ctx))
	// empty token list must not pollute the context
	require.Nil(t, betaSelfHealDropTokens(withBetaSelfHealDrop(context.Background(), nil)))
}

func TestTkIsAnthropicUsagePolicyBlock(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		body string
		want bool
	}{
		{
			name: "usage policy violation (claude-code#60366)",
			msg:  "Claude Code is unable to respond to this request, which appears to violate our Usage Policy (https://www.anthropic.com/legal/aup). This request triggered cyber-related safeguards.",
			want: true,
		},
		{
			name: "cyber verification program",
			body: `{"error":{"message":"... fill out the Cyber Verification Program form ..."}}`,
			want: true,
		},
		{
			name: "high-risk cyber marker",
			msg:  "request blocked: high-risk cyber content",
			want: true,
		},
		{
			name: "generic rate limit is not a policy block",
			msg:  "rate_limit_error: usage limit reached",
			want: false,
		},
		{
			name: "empty",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tkIsAnthropicUsagePolicyBlock(tc.msg, []byte(tc.body)))
		})
	}
}

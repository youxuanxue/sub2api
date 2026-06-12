package handler

import "testing"

// The output-token ceiling feeds the hold's upper bound, so missing a surface's
// field name silently under-caps nothing (fallback is huge) but over-caps cost:
// every supported field spelling must be honoured, and absence must fall back
// to the conservative ceiling.
func TestTkParseMaxOutputTokens(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"chat max_tokens", `{"max_tokens":1024}`, 1024},
		{"chat max_completion_tokens", `{"max_completion_tokens":2048}`, 2048},
		{"responses max_output_tokens", `{"max_output_tokens":4096}`, 4096},
		{"max of multiple fields", `{"max_tokens":100,"max_output_tokens":300,"max_completion_tokens":200}`, 300},
		{"absent falls back", `{}`, tkHoldFallbackMaxOutputTokens},
		{"zero falls back", `{"max_tokens":0}`, tkHoldFallbackMaxOutputTokens},
		{"negative falls back", `{"max_tokens":-5}`, tkHoldFallbackMaxOutputTokens},
	}
	for _, tc := range cases {
		if got := tkParseMaxOutputTokens([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: tkParseMaxOutputTokens(%s) = %d, want %d", tc.name, tc.body, got, tc.want)
		}
	}
}

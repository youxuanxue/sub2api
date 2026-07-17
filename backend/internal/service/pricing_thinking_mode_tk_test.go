//go:build unit

package service

import "testing"

// TestTkThinkingModeActiveFromBody pins the money-critical default: enable_thinking
// is treated as active UNLESS explicitly false, mirroring Alibaba DashScope's
// default-true behavior for open-source dense Qwen3. A regression flipping this to
// default-false would silently under-bill the default traffic at the non-thinking
// rate, so the default case is the most important assertion here.
func TestTkThinkingModeActiveFromBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"absent defaults to thinking", `{"model":"qwen3-8b","messages":[]}`, true},
		{"explicit true", `{"model":"qwen3-8b","enable_thinking":true}`, true},
		{"explicit false", `{"model":"qwen3-8b","enable_thinking":false}`, false},
		{"string false", `{"model":"qwen3-8b","enable_thinking":"false"}`, false},
		{"string true", `{"model":"qwen3-8b","enable_thinking":"true"}`, true},
		// A client that wrongly nests the flag under extra_body sends no top-level
		// key — upstream ignores extra_body too and falls back to its default (on),
		// so TK's default-true keeps billing in lockstep with what upstream charges.
		{"nested under extra_body is not read (matches upstream default)", `{"model":"qwen3-8b","extra_body":{"enable_thinking":false}}`, true},
		{"empty body defaults to thinking", ``, true},
		{"malformed body defaults to thinking", `{not json`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tkThinkingModeActiveFromBody([]byte(tc.body)); got != tc.want {
				t.Fatalf("tkThinkingModeActiveFromBody(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

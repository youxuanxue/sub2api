//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalizeOpenAICompatRoutingModel(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"gpt5.4-mini", "gpt-5.4-mini"},
		{" GPT-5.4-Mini ", "gpt-5.4-mini"},
		{"gpt-5.4-mini", "gpt-5.4-mini"},
		{"gpt-5", "gpt-5.5"},
		{"gpt-5-chat", "gpt-5.5"},
		{"gpt-5-chat-latest", "gpt-5.5"},
		{"gpt-5.5-pro", "gpt-5.5"},
		{"gpt-5-mini", "gpt-5.4"},
		{"gpt-5-nano", "gpt-5.4"},
		{"gpt-5.1", "gpt-5.4"},
		{"gpt-5.4-high", "gpt-5.4"},
		{"gpt-5.3", "gpt-5.3-codex-spark"},
		{"gpt-5.3-chat-latest", "gpt-5.3-codex-spark"},
		{"gpt-5.3-codex", "gpt-5.3-codex-spark"},
		{"gpt-5.3-codex-xhigh", "gpt-5.3-codex-spark"},
		{"codex-mini-latest", "gpt-5.3-codex-spark"},
		{"gpt-5-codex", "gpt-5.3-codex-spark"},
		{"qwen-max", "qwen-max"},
		{"", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, CanonicalizeOpenAICompatRoutingModel(tc.in))
		})
	}
}

func TestNormalizeKnownOpenAICodexModel_BareGPT56RoutesToSol(t *testing.T) {
	tests := map[string]string{
		"gpt-5.6":            "gpt-5.6-sol",
		"openai/gpt-5.6":     "gpt-5.6-sol",
		"gpt5.6":             "gpt-5.6-sol",
		"gpt-5.6-high":       "gpt-5.6-sol",
		"gpt-5.6-max":        "gpt-5.6-sol",
		"gpt-5.6-2026-07-09": "gpt-5.6-sol",
		"openai/gpt-5.6-max": "gpt-5.6-sol",
	}

	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, expected, normalizeKnownOpenAICodexModel(input))
		})
	}
}

func TestUsageBillingModelCandidates_BareGPT56IncludesSol(t *testing.T) {
	require.Equal(t,
		[]string{"gpt-5.6", "gpt-5.6-sol"},
		usageBillingModelCandidates("gpt-5.6"),
	)
	require.Equal(t,
		[]string{"openai/gpt-5.6", "gpt-5.6", "gpt-5.6-sol"},
		usageBillingModelCandidates("openai/gpt-5.6"),
	)
}

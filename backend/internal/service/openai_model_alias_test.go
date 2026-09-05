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
		{"gpt-5-mini", "gpt-5.5"},
		{"gpt-5-nano", "gpt-5.5"},
		{"gpt-5.1", "gpt-5.5"},
		{"gpt-5.4-high", "gpt-5.5"},
		{"gpt-5.6", "gpt-5.6-sol"},
		{"gpt-5.4", "gpt-5.5"},
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

func TestOpenAIWireBillingEffortStripInvariants(t *testing.T) {
	t.Parallel()

	// Effort strip must keep the billing family; entitlement wire remap is
	// upstream-only. This is the invariant that twice broke CI when remap
	// leaked into NormalizeOpenAICompatRequestedModel.
	require.Equal(t, "gpt-5.4", NormalizeOpenAICompatRequestedModel("gpt-5.4-xhigh"))
	require.Equal(t, "gpt-5.4", normalizeOpenAIBillingModel("gpt-5.4-xhigh"))
	require.Equal(t, "gpt-5.5", CanonicalizeOpenAICompatRoutingModel("gpt-5.4"))

	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.Equal(t, "gpt-5.5", normalizeOpenAIModelForUpstream(oauth, "gpt-5.4"))
	require.Equal(t, []string{"gpt-5.4", "gpt-5.5"}, openAIChannelRestrictionModelCandidates("gpt-5.4"))
}

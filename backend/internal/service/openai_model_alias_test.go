//go:build unit

package service

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalizeOpenAICompatRoutingModel(t *testing.T) {
	t.Parallel()

	// Positive samples are projected from the wire owner (codexModelMap). Do not
	// hand-maintain a second GPT remap table here (test SSOT).
	keys := make([]string, 0, len(codexModelMap))
	for in := range codexModelMap {
		keys = append(keys, in)
	}
	sort.Strings(keys)
	for _, in := range keys {
		require.Equal(t, codexModelMap[in], CanonicalizeOpenAICompatRoutingModel(in), in)
	}

	// Boundary samples: spelling / non-owner ids the map cannot express.
	boundaries := []struct {
		in   string
		want string
	}{
		{"gpt5.4-mini", "gpt-5.6-luna"},
		{" GPT-5.4-Mini ", "gpt-5.6-luna"},
		{"gpt5.4mini", "gpt-5.6-luna"},
		{"gpt 5.4 mini", "gpt-5.6-luna"},
		{"gpt5.4", "gpt-5.6-terra"},
		{"gpt 5.4", "gpt-5.6-terra"},
		{"qwen-max", "qwen-max"},
		{"", ""},
	}
	for _, tc := range boundaries {
		tc := tc
		t.Run("boundary/"+tc.in, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, CanonicalizeOpenAICompatRoutingModel(tc.in))
		})
	}
}

func TestOpenAIWireBillingEffortStripInvariants(t *testing.T) {
	t.Parallel()

	// Effort strip must keep the mapping/family id; entitlement wire remap is
	// upstream-only. Settlement then follows the admitted upstream model.
	require.Equal(t, "gpt-5.4", NormalizeOpenAICompatRequestedModel("gpt-5.4-xhigh"))
	require.Equal(t, "gpt-5.4", normalizeOpenAIBillingModel("gpt-5.4-xhigh"))
	require.Equal(t, "gpt-5.4-mini", normalizeOpenAIBillingModel("gpt-5.4-mini"))

	wantBare := codexModelMap["gpt-5.4"]
	wantMini := codexModelMap["gpt-5.4-mini"]
	require.Equal(t, wantBare, CanonicalizeOpenAICompatRoutingModel("gpt-5.4"))
	require.Equal(t, wantMini, CanonicalizeOpenAICompatRoutingModel("gpt-5.4-mini"))

	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.Equal(t, wantBare, normalizeOpenAIModelForUpstream(oauth, "gpt-5.4"))
	require.Equal(t, wantMini, normalizeOpenAIModelForUpstream(oauth, "gpt-5.4-mini"))
	require.Equal(t, wantBare, normalizeOpenAIBillingModel(wantBare))
	require.Equal(t, wantMini, normalizeOpenAIBillingModel(wantMini))

	billing, upstream := resolveOpenAIForwardMappedModels(oauth, "gpt-5.4", false)
	require.Equal(t, wantBare, upstream)
	require.Equal(t, wantBare, billing, "OAuth must bill the served Terra/Luna tier, not the retired 5.4 card")
	billing, upstream = resolveOpenAIForwardMappedModels(oauth, "gpt-5.4-mini", false)
	require.Equal(t, wantMini, upstream)
	require.Equal(t, wantMini, billing)

	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	billing, upstream = resolveOpenAIForwardMappedModels(apiKey, "gpt-5.4", false)
	require.Equal(t, "gpt-5.4", upstream)
	require.Equal(t, "gpt-5.4", billing, "API-key paths that still send gpt-5.4 keep gpt-5.4 settlement")

	require.Equal(t, []string{"gpt-5.4", wantBare}, openAIChannelRestrictionModelCandidates("gpt-5.4"))
	require.Equal(t, []string{"gpt-5.4-mini", wantMini}, openAIChannelRestrictionModelCandidates("gpt-5.4-mini"))
}

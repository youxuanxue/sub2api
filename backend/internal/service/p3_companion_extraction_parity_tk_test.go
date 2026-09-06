package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// P3 companion extraction parity: pins thin *_tk_* seams so a future edit cannot
// drop or invert the moved hot-path wiring while underlying helpers still pass.

func TestP3Companion_WindowRecoveryNeverEmpty(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	a := &Account{ID: 1, Name: "a"}
	b := &Account{ID: 2, Name: "b"}

	// Already have candidates → windowDropped ignored.
	got := tkRecoverAnthropicCandidatesFromWindowDropped([]*Account{a}, []*Account{b}, now)
	require.Equal(t, []*Account{a}, got)

	// Empty candidates + empty dropped → stay empty.
	got = tkRecoverAnthropicCandidatesFromWindowDropped(nil, nil, now)
	require.Empty(t, got)

	// Empty candidates + dropped → recover via leastUtilizedAnthropicAccount.
	got = tkRecoverAnthropicCandidatesFromWindowDropped(nil, []*Account{a}, now)
	require.Len(t, got, 1)
	require.Equal(t, int64(1), got[0].ID)
}

func TestP3Companion_AllowOrCollectWindowCost(t *testing.T) {
	svc := &GatewayService{}
	acc := &Account{ID: 7, Platform: PlatformAnthropic}
	var dropped []*Account

	// Anthropic window gate without OAuth/setup-token identity stays schedulable
	// (isAccountSchedulableForAnthropicWindow returns true for non-window accounts).
	ok := svc.tkAllowOrCollectWindowCost(t.Context(), acc, &dropped)
	require.True(t, ok)
	require.Empty(t, dropped)
}

func TestP3Companion_GatewayForwardingExtrasDefaults(t *testing.T) {
	d := tkDefaultGatewayForwardingExtras()
	require.True(t, d.anthropicRequestNormalize)
	require.False(t, d.canonicalIngressStrict)
	require.False(t, d.canonicalHaikuMimicry)

	parsed := tkParseGatewayForwardingExtras(map[string]string{
		SettingKeyAnthropicRequestNormalizeEnabled:         "false",
		SettingKeyAnthropicCanonicalIngressStrictEnabled:   "true",
		SettingKeyAnthropicCanonicalHaikuMimicryEnabled:    "true",
	})
	require.False(t, parsed.anthropicRequestNormalize)
	require.True(t, parsed.canonicalIngressStrict)
	require.True(t, parsed.canonicalHaikuMimicry)

	keys := tkGatewayForwardingExtraSettingKeys()
	require.Contains(t, keys, SettingKeyAnthropicRequestNormalizeEnabled)
	require.Contains(t, keys, SettingKeyAnthropicCanonicalIngressStrictEnabled)
	require.Contains(t, keys, SettingKeyAnthropicCanonicalHaikuMimicryEnabled)
}

func TestP3Companion_PublicSignupPricingParseApply(t *testing.T) {
	src := tkParsePublicSignupPricing(map[string]string{
		SettingKeySignupBonusEnabled:  "true",
		SettingKeySignupBonusBalance:  "1.25",
		SettingKeyPricingCatalogPublic: "false",
	})
	require.True(t, src.SignupBonusEnabled)
	require.InDelta(t, 1.25, src.SignupBonusBalanceDisplayUSD, 1e-9)
	require.False(t, src.PricingCatalogPublic)

	disabled := tkParsePublicSignupPricing(map[string]string{
		SettingKeySignupBonusEnabled: "false",
		SettingKeySignupBonusBalance: "9",
	})
	require.False(t, disabled.SignupBonusEnabled)
	require.Equal(t, 0.0, disabled.SignupBonusBalanceDisplayUSD)

	out := &PublicSettings{}
	tkApplyPublicSignupPricing(out, src)
	require.True(t, out.SignupBonusEnabled)
	require.InDelta(t, 1.25, out.SignupBonusBalanceDisplayUSD, 1e-9)
	require.False(t, out.PricingCatalogPublic)

	payload := &PublicSettingsInjectionPayload{}
	tkApplyPublicSignupPricingInjection(payload, out)
	require.True(t, payload.SignupBonusEnabled)
	require.InDelta(t, 1.25, payload.SignupBonusBalanceUSD, 1e-9)
	require.False(t, payload.PricingCatalogPublic)
}

func TestP3Companion_ToolSearchPrefilterUsesGetBody(t *testing.T) {
	const model = "claude-opus-4-8"
	body := []byte(`{"model":"claude-opus-4-8","tools":[{"type":"tool_search_tool_regex"}],"messages":[]}`)
	var seen []byte
	err := tkApplyToolSearchHistoricalThinkingPrefilter(model,
		func() []byte { return body },
		func(next []byte) error {
			seen = append([]byte(nil), next...)
			body = next
			return nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, TkPrefilterToolSearchHistoricalThinking(body, model), seen)
}

func TestP3Companion_CloudwiseSkipORsBothSignals(t *testing.T) {
	// Empty canonical model → balance-402 path cannot match; provider-424 alone may.
	// With nil account both helpers are false → skip=false (permanent block allowed).
	require.False(t, tkCloudwiseSkipPermanentRuntimeBlock(nil, http.StatusPaymentRequired, nil, nil))
	require.False(t, tkCloudwiseSkipPermanentRuntimeBlock(nil, http.StatusFailedDependency, nil, []string{"gpt-5.4"}))
}

func TestP3Companion_FallbackTableFamilyViaGetFallbackPricing(t *testing.T) {
	svc := newTestBillingService()
	// Moved family matcher must remain reachable through getFallbackPricing.
	fable := svc.getFallbackPricing("claude-fable-5")
	require.NotNil(t, fable)
	require.InDelta(t, 10e-6, fable.InputPricePerToken, 1e-12)

	generic := svc.getFallbackPricing("claude-unknown-x")
	require.NotNil(t, generic)
	require.InDelta(t, 3e-6, generic.InputPricePerToken, 1e-12)

	g31 := svc.getFallbackPricing("gemini-3.1-pro-preview")
	require.NotNil(t, g31)
	direct := svc.tkResolveFallbackTableFamilyPricing("gemini-3.1-pro-preview")
	require.NotNil(t, direct)
	require.Equal(t, g31.InputPricePerToken, direct.InputPricePerToken)
}

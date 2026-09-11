package protocolrouter

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGeminiToChatPlanChecksActualBody(t *testing.T) {
	router := New(allTestAdapters())
	for _, body := range []string{
		`{"contents":[{"parts":[{"inlineData":{"data":"AA=="}}]}]}`,
		`{"contents":[{"parts":[{"text":"hi","thoughtSignature":"opaque"}]}]}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"cachedContent":"cache"}`,
	} {
		r, err := ParseCanonicalRequest(ProtocolGeminiGenerateContent, ResponsesPathNone, "client-model", false, []byte(body))
		require.NoError(t, err)
		_, err = router.Plan(r, testAccount(t, ProtocolChatCompletions))
		require.ErrorIs(t, err, ErrNoLegalRoute)
		p, err := router.Plan(r, testAccount(t, ProtocolGeminiGenerateContent))
		require.NoError(t, err)
		require.Equal(t, AdapterGeminiIdentity, p.AdapterID(), "conversion rejection must not change native behavior")
	}
	r, err := ParseCanonicalRequest(ProtocolGeminiGenerateContent, ResponsesPathNone, "client-model", true, []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
	require.NoError(t, err)
	p, err := router.Plan(r, testAccount(t, ProtocolChatCompletions))
	require.NoError(t, err)
	require.Equal(t, AdapterGeminiToChat, p.AdapterID())
	require.Equal(t, RouteConversion, p.RouteKind())
	require.Equal(t, r.Digest(), p.RequestDigest())
	require.Zero(t, p.CompatibilityRank())
	_, err = router.PlanNative(r, testAccount(t, ProtocolChatCompletions))
	require.ErrorIs(t, err, ErrNoLegalRoute)
	p, err = router.Plan(r, testAccount(t, ProtocolChatCompletions, ProtocolGeminiGenerateContent))
	require.NoError(t, err)
	require.Equal(t, AdapterGeminiIdentity, p.AdapterID())
}

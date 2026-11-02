package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkSanitizeUpstreamProductWireMessage(t *testing.T) {
	t.Parallel()

	public, sanitized := TkSanitizeUpstreamProductWireMessage(
		"The requested model does not support the agent plan feature. Please refer to the documentation at https://www.volcengine.com/docs/82379/2366394",
	)
	require.True(t, sanitized)
	require.Equal(t, TkClientFacingUnsupportedMessage, public)

	public, sanitized = TkSanitizeUpstreamProductWireMessage("agent plan TTS response_format must be mp3, pcm or opus")
	require.True(t, sanitized)
	require.Equal(t, TkClientFacingUnsupportedMessage, public)

	public, sanitized = TkSanitizeUpstreamProductWireMessage("Ali Token Plan quota exhausted for this model")
	require.True(t, sanitized)
	require.Equal(t, TkClientFacingUnsupportedMessage, public)

	public, sanitized = TkSanitizeUpstreamProductWireMessage(TkClientFacingUnsupportedMessage)
	require.False(t, sanitized)
	require.Equal(t, TkClientFacingUnsupportedMessage, public)

	public, sanitized = TkSanitizeUpstreamProductWireMessage("Your input exceeds the context window of this model.")
	require.False(t, sanitized)
	require.Equal(t, "Your input exceeds the context window of this model.", public)

	public, sanitized = TkSanitizeUpstreamProductWireMessage(TkClientFacingBusy429Message)
	require.False(t, sanitized)
	require.Equal(t, TkClientFacingBusy429Message, public)
}

func TestTkLooksLikeUpstreamProductLeakMessage(t *testing.T) {
	t.Parallel()
	require.True(t, TkLooksLikeUpstreamProductLeakMessage("TOKEN-PLAN unavailable"))
	require.True(t, TkLooksLikeUpstreamProductLeakMessage("see https://www.volcengine.com/docs/82379/x"))
	require.False(t, TkLooksLikeUpstreamProductLeakMessage("model_not_found"))
}

// Prod 2026-11-02: Volc Ark expired-subscription 400 leaked "AgentPlan" (no
// space) and console.volcengine.com because neither matched a needle.
const tkVolcAgentPlanExpiredMessage = "Your account (2131399903) does not have a valid AgentPlan subscription, or your subscription has expired. Please visit https://console.volcengine.com/ark/region:ark+cn-beijing/openManagement?OpenModelVisible=false&tab=CodingPlan to review your subscription status or complete the subscription or renewal process. Request id: 021793556394017fa0eb8de5025091839561e6fd538adcb9e5394"

func TestTkSanitizeUpstreamProductWireMessage_VolcAgentPlanSubscriptionExpired(t *testing.T) {
	t.Parallel()
	public, sanitized := TkSanitizeUpstreamProductWireMessage(tkVolcAgentPlanExpiredMessage)
	require.True(t, sanitized)
	require.Equal(t, TkClientFacingUnsupportedMessage, public)

	require.True(t, TkLooksLikeUpstreamProductLeakMessage("Coding Plan quota exhausted"))
	require.True(t, TkLooksLikeUpstreamProductLeakMessage("see https://ark.cn-beijing.volces.com/api/plan/v3"))
}

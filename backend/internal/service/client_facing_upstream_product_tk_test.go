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

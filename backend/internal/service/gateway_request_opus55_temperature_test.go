//go:build unit

package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpus55NonDefaultTemperature_NormalizedNotRejected(t *testing.T) {
	// Policy A: Opus 5.5 defaults to adaptive thinking and Anthropic rejects
	// non-default temperature, but the gateway normalizes on the Anthropic
	// newapi bridge instead of returning a local client 400.
	body := []byte(`{"model":"claude-opus-5-5","temperature":0.2,"messages":[{"role":"user","content":"hi"}]}`)
	require.NoError(t, validateClaude55Request(body, "claude-opus-5-5"))

	got := applyNewAPIAnthropicThinkingTemperature(
		&Account{ChannelType: newapiconstant.ChannelTypeAnthropic},
		"claude-opus-5-5",
		body,
	)
	require.InDelta(t, 1.0, gjson.GetBytes(got, "temperature").Float(), 1e-9)
	require.Equal(t, "hi", gjson.GetBytes(got, "messages.0.content").String())
}

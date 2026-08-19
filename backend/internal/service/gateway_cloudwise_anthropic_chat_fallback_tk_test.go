//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
)

func TestShouldForwardCloudwiseAnthropicViaChatCompletions_PreservesModelSplit(t *testing.T) {
	account := cloudwiseNativeMessagesAccount()
	account.Platform = PlatformAnthropic
	account.Extra[openai_compat.ExtraKeyNativeMessagesSupported] = true

	claude, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-6","messages":[]}`)), PlatformAnthropic)
	require.NoError(t, err)
	require.False(t, shouldForwardCloudwiseAnthropicViaChatCompletions(account, claude))

	nonClaude, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"glm-5.3","messages":[]}`)), PlatformAnthropic)
	require.NoError(t, err)
	require.True(t, shouldForwardCloudwiseAnthropicViaChatCompletions(account, nonClaude))
}

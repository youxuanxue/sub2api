//go:build unit

package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

// Regression: a newapi (fifth platform) Qwen/DashScope account must never have
// its channel credential sent to api.openai.com.
//
// Inbound /v1/messages reached ForwardAsAnthropic without the bridge dispatcher,
// so a newapi api-key account fell through tkTryRouteForwardAsAnthropic's last
// predicate into the OpenAI-shaped raw Chat Completions fallback. That path
// resolves its upstream from OpenAI-family accessors, which return "" for
// platform=newapi, and the caller turned "" into the official OpenAI host while
// still sending the account's real DashScope key — producing upstream
// "Incorrect API key provided", the exact shape
// IsForeignCredentialOfficialOpenAIReject already classifies as a routing
// defect rather than a dead credential.
func newapiQwenAccountForLeakTest(name string, id int64) *Account {
	return &Account{
		ID:          id,
		Name:        name,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"api_key":       "sk-dashscope-not-an-openai-key",
			"base_url":      "https://dashscope.aliyuncs.com",
			"model_mapping": map[string]any{"qwen3-max": "qwen3-max"},
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

// The credential does reach the wire on the native path, so the destination is
// the only thing standing between a DashScope key and api.openai.com.
func TestNewAPIQwenCredentialIsWireBoundOnNativePath(t *testing.T) {
	for _, name := range []string{"Qwen", "Qwen-2", "Qwen-4"} {
		t.Run(name, func(t *testing.T) {
			account := newapiQwenAccountForLeakTest(name, 60)
			require.Equal(t, "sk-dashscope-not-an-openai-key", account.GetOpenAIProtocolAPIKey())
			require.False(t, AccountUsesOfficialOpenAIUpstream(account))
			require.False(t, OfficialOpenAIFallbackAllowed(account),
				"a newapi channel credential must never be allowed to default to api.openai.com")
		})
	}
}

// Primary fix: inbound /v1/messages for a bridge-eligible newapi account is
// owned by the NewAPI adaptor, never by an OpenAI-shaped fallback.
func TestNewAPIBridgeOwnsAnthropicMessagesForQwen(t *testing.T) {
	svc := &OpenAIGatewayService{}

	for _, name := range []string{"Qwen", "Qwen-2", "Qwen-4"} {
		require.True(t, svc.newAPIBridgeOwnsAnthropicMessages(newapiQwenAccountForLeakTest(name, 60)),
			"inbound /v1/messages for %s must relay through the NewAPI adaptor", name)
	}

	// An OpenAI api-key account keeps the existing OpenAI-shaped fallbacks.
	require.False(t, svc.newAPIBridgeOwnsAnthropicMessages(&Account{
		ID:          76,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-openai"},
	}))

	// A newapi row with no channel type cannot enter the bridge.
	require.False(t, svc.newAPIBridgeOwnsAnthropicMessages(&Account{
		ID:          61,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 0,
		Credentials: map[string]any{"api_key": "sk-x", "base_url": "https://dashscope.aliyuncs.com"},
	}))
}

// Defense in depth: even a newapi row that cannot enter the bridge (channel_type
// unset, so ShouldDispatchToNewAPIBridge is false) must fail closed rather than
// POST its foreign credential to the official OpenAI host.
func TestChatCompletionsTargetFailsClosedForForeignCredential(t *testing.T) {
	svc := &OpenAIGatewayService{}

	offBridge := &Account{
		ID:          61,
		Name:        "Qwen-no-channel-type",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 0,
		Credentials: map[string]any{
			"api_key":  "sk-dashscope-not-an-openai-key",
			"base_url": "https://dashscope.aliyuncs.com",
		},
	}
	_, err := svc.openAIChatCompletionsTargetURL(offBridge)
	require.ErrorIs(t, err, ErrForeignCredentialOfficialOpenAIFallback,
		"a foreign credential with no OpenAI-resolvable base must not fall back to api.openai.com")

	// An OpenAI api-key account still resolves the official host as before.
	official := &Account{
		ID:          76,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-openai"},
	}
	targetURL, err := svc.openAIChatCompletionsTargetURL(official)
	require.NoError(t, err)
	require.Contains(t, targetURL, "api.openai.com")
}

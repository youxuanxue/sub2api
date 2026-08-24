//go:build unit

package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

// Regression for the 2026-08-24 prod incident: inbound /v1/chat/completions for a
// bridge-eligible newapi account must be owned by the NewAPI adaptor.
//
// Same defect class as #1800 (/v1/messages), one endpoint over. account 39
// "ds-官" (platform=newapi, channel_type=43 DeepSeek, base_url set, status
// active, schedulable, zero load) failed 100% of requests in ~35ms because every
// OpenAI-shaped forwarder resolves its upstream from OpenAI-family accessors
// that return "" for platform=newapi, and #1800's guard then correctly refuses
// to default that "" to api.openai.com. The account was healthy; the routing was
// wrong. Its group had no other member, so each failure emptied the pool and the
// next request fast-failed with routing 429 "No available accounts".

func newapiDeepSeekAccountForBridgeTest() *Account {
	return &Account{
		ID:          39,
		Name:        "ds-官",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeDeepSeek,
		Credentials: map[string]any{
			"api_key":  "sk-deepseek-not-an-openai-key",
			"base_url": "https://api.deepseek.com",
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

// The prod account, reconstructed: without bridge ownership its upstream cannot
// be resolved at all, which is why the request died before any network I/O.
func TestNewAPIDeepSeekChatCompletionsWouldFailClosedWithoutBridge(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := newapiDeepSeekAccountForBridgeTest()

	// The credential is real and wire-bound; only the destination was wrong.
	require.Equal(t, "sk-deepseek-not-an-openai-key", account.GetOpenAIProtocolAPIKey())
	require.False(t, AccountUsesOfficialOpenAIUpstream(account))
	require.False(t, OfficialOpenAIFallbackAllowed(account),
		"a newapi channel credential must never be allowed to default to api.openai.com")

	// This is the exact prod failure: the OpenAI-shaped resolver has nothing to
	// resolve, so #1800's guard refuses the request.
	_, err := svc.openAIChatCompletionsTargetURL(account)
	require.ErrorIs(t, err, ErrForeignCredentialOfficialOpenAIFallback,
		"the OpenAI-shaped path cannot serve this account; the bridge must own it")
}

// Primary fix: the account above is claimed by the bridge before any
// OpenAI-shaped fallback can reach it.
func TestNewAPIBridgeOwnsChatCompletionsForDeepSeek(t *testing.T) {
	svc := &OpenAIGatewayService{}

	require.True(t, svc.newAPIBridgeOwnsChatCompletions(newapiDeepSeekAccountForBridgeTest()),
		"inbound /v1/chat/completions for a bridge-eligible newapi account must relay through the adaptor")

	// Ali/DashScope channels ride the same rule — this is not DeepSeek-specific.
	require.True(t, svc.newAPIBridgeOwnsChatCompletions(&Account{
		ID:          78,
		Name:        "Qwen-4",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{"api_key": "sk-dashscope", "base_url": "https://dashscope.aliyuncs.com"},
	}))
}

// Non-newapi accounts keep their existing routing untouched.
func TestNewAPIBridgeOwnsChatCompletionsLeavesOtherPlatformsAlone(t *testing.T) {
	svc := &OpenAIGatewayService{}

	require.False(t, svc.newAPIBridgeOwnsChatCompletions(nil))

	require.False(t, svc.newAPIBridgeOwnsChatCompletions(&Account{
		ID:          76,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-openai"},
	}), "an OpenAI api-key account must keep the OpenAI-shaped path")

	require.False(t, svc.newAPIBridgeOwnsChatCompletions(&Account{
		ID:       71,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
	}))

	// A newapi row with no channel type cannot enter the bridge; #1800's
	// fail-closed guard remains its only protection, which is correct.
	require.False(t, svc.newAPIBridgeOwnsChatCompletions(&Account{
		ID:          61,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 0,
		Credentials: map[string]any{"api_key": "sk-x", "base_url": "https://dashscope.aliyuncs.com"},
	}))
}

// The VolcEngine Agent Plan carve-out must survive: its /api/plan/v3 endpoint is
// a direct OpenAI-compatible upstream and stays on TokenKey's native path, since
// the upstream adaptor would append its own /api/v3 suffix.
//
// This is load-bearing for the incident's own workaround: account 88 was added
// to the affected group as a stopgap and serves traffic through the native path.
// Claiming it for the bridge here would break the mitigation.
func TestNewAPIBridgeDoesNotClaimVolcEngineAgentPlan(t *testing.T) {
	svc := &OpenAIGatewayService{}

	agentPlan := &Account{
		ID:          88,
		Name:        "volcengine-agent-plan",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"api_key":  "ark-key",
			"base_url": "https://ark.cn-beijing.volces.com/api/plan/v3",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	require.True(t, isNewAPIVolcEngineAgentPlanAccount(agentPlan),
		"test fixture must reproduce the agent-plan shape")
	require.False(t, svc.newAPIBridgeOwnsChatCompletions(agentPlan),
		"agent plan must keep the native path; the adaptor would append /api/v3")

	// And it resolves an upstream on the native path, which is why it works.
	targetURL, err := svc.openAIChatCompletionsTargetURL(agentPlan)
	require.NoError(t, err)
	require.Contains(t, targetURL, "ark.cn-beijing.volces.com")
}

// The routing helper reports handled=false for accounts it does not own, so the
// caller's existing chain proceeds unchanged.
func TestTKTryRouteChatCompletionsViaNewAPIBridgeSkipsUnownedAccounts(t *testing.T) {
	svc := &OpenAIGatewayService{}

	result, handled, err := svc.tkTryRouteChatCompletionsViaNewAPIBridge(
		nil, nil,
		&Account{ID: 76, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		[]byte(`{"model":"gpt-5","messages":[]}`), "", "",
	)
	require.False(t, handled, "an unowned account must fall through to the existing routing chain")
	require.NoError(t, err)
	require.Nil(t, result)
}

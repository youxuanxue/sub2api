//go:build unit

package service

import (
	"context"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestQwenTokenPlanPreservesThinkingIntent(t *testing.T) {
	account := &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 17, Credentials: map[string]any{"base_url": newapiintegration.AliTokenPlanBaseURL}}
	for _, tc := range []struct {
		name, body string
		want       string
	}{
		{"buffered_reasoning", `{"stream":false,"reasoning_effort":"low","max_completion_tokens":16384}`, "true"},
		{"explicit_off", `{"stream":false,"enable_thinking":false}`, "false"},
		{"effort_none", `{"reasoning_effort":"none"}`, "false"},
		{"forced_tool_without_thinking", `{"tool_choice":{"type":"function","function":{"name":"echo"}}}`, "false"},
		{"required_tool_without_thinking", `{"tool_choice":"required"}`, "false"},
		{"thinking_precedes_forced_tool", `{"reasoning_effort":"low","tool_choice":"required"}`, "true"},
		{"explicit_on", `{"stream":false,"enable_thinking":true}`, "true"},
		{"auto_preserves_provider_default", `{"tool_choice":"auto"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := applyNewAPIQwenAccountShape(account, "qwen3.7-plus", []byte(tc.body))
			require.Equal(t, tc.want, gjson.GetBytes(got, "enable_thinking").Raw)
			require.Equal(t, gjson.Get(tc.body, "reasoning_effort").Raw, gjson.GetBytes(got, "reasoning_effort").Raw)
			require.Equal(t, gjson.Get(tc.body, "max_completion_tokens").Raw, gjson.GetBytes(got, "max_completion_tokens").Raw)
			require.Equal(t, gjson.Get(tc.body, "stream").Raw, gjson.GetBytes(got, "stream").Raw)
		})
	}
	account.Credentials["base_url"] = "https://dashscope.aliyuncs.com"
	got := applyNewAPIQwenAccountShape(account, "qwen3-8b", []byte(`{"stream":false,"enable_thinking":true}`))
	require.False(t, gjson.GetBytes(got, "enable_thinking").Bool(), "legacy DashScope workaround stays scoped to its provider")
}

func TestQwenPlannedAliasIsResolvedBeforeProviderShaping(t *testing.T) {
	account := &Account{ID: 129, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 17,
		Credentials: map[string]any{"base_url": newapiintegration.AliTokenPlanBaseURL, "api_key": "test-key", "model_mapping": map[string]any{"qwen-plus": "qwen3.7-plus"}}}
	attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions)
	body := []byte(`{"model":"qwen-plus","messages":[{"role":"user","content":"Call echo"}],"tool_choice":"required","tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object"}}}],"stream":false}`)
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{InboundProtocol: protocolrouter.ProtocolChatCompletions, RequestedModel: "qwen-plus", Body: body})
	require.NoError(t, err)
	snapshot, err := protocolAccountSnapshotForRequest(account, request)
	require.NoError(t, err)
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	require.NoError(t, err)
	old := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = old })
	calls := 0
	dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, in bridge.ChannelContextInput, wire []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		calls++
		require.Equal(t, "qwen3.7-plus", gjson.GetBytes(wire, "model").String())
		require.Equal(t, "false", gjson.GetBytes(wire, "enable_thinking").Raw)
		require.Equal(t, "required", gjson.GetBytes(wire, "tool_choice").String())
		return &bridge.DispatchOutcome{Model: "qwen3.7-plus"}, nil
	}
	c, _ := gin.CreateTestContext(nil)
	_, err = (&OpenAIGatewayService{}).ForwardAsChatCompletionsDispatched(withProtocolExecutionPlan(context.Background(), plan), c, account, body, "", "")
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, "qwen-plus", gjson.GetBytes(body, "model").String(), "retry input remains immutable")
}

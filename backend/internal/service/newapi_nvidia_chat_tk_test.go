//go:build unit

package service

import (
	"context"
	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"testing"
)

func TestNVIDIATokenLimitUsesOriginalBudgetOnChatDispatch(t *testing.T) {
	old := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = old })
	dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, _ bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		require.False(t, gjson.GetBytes(body, "max_completion_tokens").Exists())
		require.EqualValues(t, 2048, gjson.GetBytes(body, "max_tokens").Int())
		require.Equal(t, "echo", gjson.GetBytes(body, "tool_choice.function.name").String())
		return &bridge.DispatchOutcome{Model: "deepseek-v4-flash"}, nil
	}
	account := &Account{ID: 138, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeOpenAI, Credentials: map[string]any{"base_url": "https://integrate.api.nvidia.com", "api_key": "test"}}
	original := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"Call echo with value OK."}],"max_tokens":32,"max_completion_tokens":2048,"tool_choice":{"type":"function","function":{"name":"echo"}},"stream":false}`)
	c, _ := gin.CreateTestContext(nil)
	_, err := (&OpenAIGatewayService{}).ForwardAsChatCompletionsDispatched(context.Background(), c, account, original, "", "")
	require.NoError(t, err)
	require.EqualValues(t, 32, gjson.GetBytes(original, "max_tokens").Int())
	require.True(t, gjson.GetBytes(original, "max_completion_tokens").Exists())
	for _, host := range []string{"https://api.openai.com", "https://integrate.api.nvidia.com.evil.example"} {
		account.Credentials["base_url"] = host
		require.Equal(t, original, applyNVIDIABuildChatTokenLimit(account, original))
	}
	account.Credentials["base_url"] = "https://integrate.api.nvidia.com"
	for _, body := range []string{`{"max_tokens":64}`, `{"max_completion_tokens":null}`, `{"max_completion_tokens":"2048"}`, `{"max_completion_tokens":-1}`} {
		require.Equal(t, []byte(body), applyNVIDIABuildChatTokenLimit(account, []byte(body)))
	}
}

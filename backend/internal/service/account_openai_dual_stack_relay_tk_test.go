//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccount_ResolveOpenAIResponsesChatFallbackBillingModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Set(OpsModelKey, "claude-sonnet-4-6")

	cloudwise := cloudwiseNativeMessagesAccount()
	require.Equal(t, "claude-sonnet-4-6", cloudwise.ResolveOpenAIResponsesChatFallbackBillingModel(c, "gpt-5.6-terra"))

	oauth := rawChatCompletionsTestAccount()
	oauth.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: true}
	require.Equal(t, "gpt-5.6-terra", oauth.ResolveOpenAIResponsesChatFallbackBillingModel(c, "gpt-5.6-terra"))

	require.Equal(t, "gpt-5.6-terra", cloudwise.ResolveOpenAIResponsesChatFallbackBillingModel(nil, "gpt-5.6-terra"))

	cGPT, _ := gin.CreateTestContext(nil)
	cGPT.Set(OpsModelKey, "gpt-5.4")
	require.Equal(t, "gpt-5.4", cloudwise.ResolveOpenAIResponsesChatFallbackBillingModel(cGPT, "gpt-5.4"))
}

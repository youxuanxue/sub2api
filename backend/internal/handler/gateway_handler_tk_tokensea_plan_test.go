package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTokenseaAnthropicMessagesUsesPlannedChat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const model = "gpt-5.6-luna"
	account := &service.Account{
		ID: 93, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"base_url": "https://agent.tokensea.ai", "api_key": "review-test-key",
			"model_mapping": map[string]any{model: model},
		},
	}
	attachHandlerTestProtocolCapability(t, account, protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions)
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
	request, err := newCanonicalProtocolRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, model, false, body)
	require.NoError(t, err)
	snapshot, err := service.ProtocolAccountSnapshot(account, model)
	require.NoError(t, err)
	router := service.NewProtocolRouter()
	_, err = router.Plan(request, snapshot)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not allowed by account")
}

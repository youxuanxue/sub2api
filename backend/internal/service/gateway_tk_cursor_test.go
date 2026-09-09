package service

import (
	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func cursorTestAccount() *Account {
	return &Account{ID: 42, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 14,
		Extra: map[string]any{CursorSourceExtraKey: "cursor"}, Credentials: map[string]any{
			"base_url": cursor.AgentBaseURL, "api_key": "cursor-user-key",
			"model_mapping":          map[string]any{"composer-2.5": "composer-2.5", "gpt-5.5": "gpt-5.5"},
			CursorWireModelsKey:      map[string]any{"composer-2.5": "composer-2.5", "gpt-5.5": "gpt-5.5"},
			CursorModelParametersKey: map[string]any{"composer-2.5": []cursor.Parameter{{ID: "fast", Value: "false"}}, "gpt-5.5": nil},
		}}
}
func cursorTestContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Set("api_key", &APIKey{ID: 8, UserID: 9})
	return c
}

func TestCursorNativeEndpointRejectsCredentialRedirection(t *testing.T) {
	account := cursorTestAccount()
	for _, endpoint := range []string{"http://attacker/v1/messages", "http://127.0.0.1:3927/v1/messages"} {
		req, err := http.NewRequest(http.MethodPost, endpoint, nil)
		require.NoError(t, err)
		require.Error(t, prepareCursorUpstreamRequest(req, cursorTestContext(), account))
	}
	req, err := http.NewRequest(http.MethodPost, cursor.AgentBaseURL+"/v1/messages", nil)
	require.NoError(t, err)
	require.NoError(t, prepareCursorUpstreamRequest(req, cursorTestContext(), account))
}
func TestCursorDoesNotEnableNonClaudeModelsForOrdinaryAnthropic(t *testing.T) {
	account := cursorTestAccount()
	require.True(t, cursorMappedModelAllowed(account, "composer-2.5", "composer-2.5"))
	require.False(t, cursorMappedModelAllowed(account, "unknown", "composer-2.5"))
	delete(account.Extra, CursorSourceExtraKey)
	require.False(t, cursorMappedModelAllowed(account, "composer-2.5", "composer-2.5"))
}

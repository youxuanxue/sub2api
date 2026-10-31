package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestDeriveResponsesPromptCacheKey_UsesStableTenantScopedDigest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 42})
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	first := []byte(`{"model":"gpt-5.6","input":[{"role":"user","content":"hello"}]}`)
	second := []byte(`{"model":"gpt-5.6","input":[{"role":"user","content":"hello"},{"role":"assistant","content":"later"}]}`)
	svc := &OpenAIGatewayService{}
	key1 := svc.deriveResponsesPromptCacheKey(c, account, first)
	key2 := svc.deriveResponsesPromptCacheKey(c, account, second)
	require.Regexp(t, `^compat_resp_[0-9a-f]{64}$`, key1)
	require.Equal(t, key1, key2)
	require.NotContains(t, key1, "hello")
}

func TestDeriveResponsesPromptCacheKey_PreservesExplicitAndUnsupportedPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 42})
	account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{}
	require.Empty(t, svc.deriveResponsesPromptCacheKey(c, account, []byte(`{"prompt_cache_key":"client" ,"input":"hello"}`)))
	require.Empty(t, svc.deriveResponsesPromptCacheKey(c, &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, []byte(`{"input":"hello"}`)))
	require.Empty(t, svc.deriveResponsesPromptCacheKey(c, account, []byte(`{"model":"gpt-5.6"}`)))
}

func TestPrepareOpenAIPassthroughRequestBody_AddsOAuthFallbackOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 42})
	svc := &OpenAIGatewayService{}
	oauth := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body, _, err := svc.tkPrepareOpenAIPassthroughRequestBody(
		context.Background(), c, oauth,
		[]byte(`{"model":"gpt-5.6","instructions":"be concise","input":[{"role":"user","content":"hello"}]}`),
		"gpt-5.6", "gpt-5.6", false,
	)
	require.NoError(t, err)
	require.Regexp(t, `^compat_resp_[0-9a-f]{64}$`, gjson.GetBytes(body, "prompt_cache_key").String())

	apiKey := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body, _, err = svc.tkPrepareOpenAIPassthroughRequestBody(
		context.Background(), c, apiKey, []byte(`{"model":"gpt-5.6","input":[{"role":"user","content":"hello"}]}`),
		"gpt-5.6", "gpt-5.6", false,
	)
	require.NoError(t, err)
	require.NotEqual(t, "compat_resp_", gjson.GetBytes(body, "prompt_cache_key").String())
}

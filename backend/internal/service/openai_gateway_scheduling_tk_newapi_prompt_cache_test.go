//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newNewAPIPromptCacheStickyContext(t *testing.T, apiKeyID int64) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("api_key", &APIKey{
		ID: apiKeyID,
		Group: &Group{
			ID:       9,
			Platform: PlatformNewAPI,
		},
	})
	return c
}

func TestGenerateSessionHash_NewAPIStablePrefixIgnoresUserTurn(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c := newNewAPIPromptCacheStickyContext(t, 626)

	first := []byte(`{"model":"glm-5.3","messages":[{"role":"system","content":"long shared prefix"},{"role":"user","content":"q1"}]}`)
	second := []byte(`{"model":"glm-5.3","messages":[{"role":"system","content":"long shared prefix"},{"role":"user","content":"q2 different"}]}`)

	h1 := svc.GenerateSessionHash(c, first)
	h2 := svc.GenerateSessionHash(c, second)
	require.NotEmpty(t, h1)
	require.Equal(t, h1, h2, "NewAPI sticky must keep the same account across independent user turns with the same system prefix")
}

func TestGenerateSessionHash_OpenAIGroupStillUsesFirstUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("api_key", &APIKey{
		ID: 626,
		Group: &Group{
			ID:       9,
			Platform: PlatformOpenAI,
		},
	})
	svc := &OpenAIGatewayService{}

	first := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"long shared prefix"},{"role":"user","content":"q1"}]}`)
	second := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"long shared prefix"},{"role":"user","content":"q2 different"}]}`)

	require.NotEqual(t, svc.GenerateSessionHash(c, first), svc.GenerateSessionHash(c, second),
		"OpenAI groups keep first-user content sticky semantics")
}

func TestGenerateSessionHash_NewAPIScopesByAPIKey(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := []byte(`{"model":"glm-5.3","messages":[{"role":"system","content":"shared"},{"role":"user","content":"hi"}]}`)

	a := svc.GenerateSessionHash(newNewAPIPromptCacheStickyContext(t, 100), body)
	b := svc.GenerateSessionHash(newNewAPIPromptCacheStickyContext(t, 200), body)
	require.NotEmpty(t, a)
	require.NotEqual(t, a, b, "different API keys must not share NewAPI sticky bindings")
}

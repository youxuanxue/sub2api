//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func cloudwiseNativeMessagesAccount() *Account {
	account := rawChatCompletionsTestAccount()
	account.ID = 95
	account.Credentials["base_url"] = "https://api.cloudwise.ai/api"
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesSupported:      false,
		openai_compat.ExtraKeyNativeMessagesSupported: true,
	}
	return account
}

func TestAccount_IsOpenAICloudwiseRelay(t *testing.T) {
	require.False(t, (*Account)(nil).IsOpenAICloudwiseRelay())

	cloudwise := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.cloudwise.ai/api",
		},
	}
	require.True(t, cloudwise.IsOpenAICloudwiseRelay())

	us := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api-us.cloudwise.ai/api/",
		},
	}
	require.True(t, us.IsOpenAICloudwiseRelay())

	other := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.openai.com/v1",
		},
	}
	require.False(t, other.IsOpenAICloudwiseRelay())
}

func TestOpenAICloudwiseRelayFloorUsesPrefixWildcards(t *testing.T) {
	mapping := openAICloudwiseRelayAccountModelMappingFloor(context.Background(), nil, nil)
	require.Equal(t, openAICloudwiseRelayWildcardModelMappingFloor(), mapping)
	for _, excluded := range []string{"gpt-5.5", "gpt-5.6", "gemini-3-flash-preview"} {
		require.NotContains(t, mapping, excluded)
	}
}

func TestForwardAsAnthropic_CloudwiseNativeMessagesPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"message","model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":7,"output_tokens":1}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, cloudwiseNativeMessagesAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://api.cloudwise.ai/api/v1/messages", upstream.lastReq.URL.String())
	require.Equal(t, "claude-sonnet-4-6", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, rec.Body.String(), "OK")
}

func TestForwardAsAnthropic_CloudwiseNonClaudeUsesChatFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"MiniMax-M3","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl-test","object":"chat.completion","model":"MiniMax-M3","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, cloudwiseNativeMessagesAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://api.cloudwise.ai/api/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "MiniMax-M3", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, rec.Body.String(), "OK")
}

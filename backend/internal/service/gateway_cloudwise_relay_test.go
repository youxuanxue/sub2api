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

	"github.com/Wei-Shaw/sub2api/internal/config"
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

// Prod account #95 has openai_responses_supported=true because the capability
// probe treats HTTP 400 as "endpoint exists". CloudWise does not implement
// /v1/responses for GLM/MiniMax (comment on IsOpenAICloudwiseRelay). Inbound
// /v1/chat/completions must stay on raw chat, otherwise CloudWise returns
// 400 "messages is invalid or missing" after CC→Responses conversion.
func TestForwardAsChatCompletions_CloudwiseNonClaudeUsesRawChatEvenWhenResponsesFlagTrue(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"glm-5.3","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_cw_glm53"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl-test","object":"chat.completion","model":"glm-5.3","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}
	account := cloudwiseNativeMessagesAccount()
	account.Extra[openai_compat.ExtraKeyResponsesSupported] = true

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://api.cloudwise.ai/api/v1/chat/completions", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").Exists(),
		"raw chat must keep messages; Responses conversion would drop them")
	require.Equal(t, "glm-5.3", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, rec.Body.String(), "OK")
}

func TestShouldForwardOpenAIResponsesViaRawChatCompletions_DualStackRelaysIgnoreResponsesFlag(t *testing.T) {
	cloudwise := cloudwiseNativeMessagesAccount()
	cloudwise.Extra[openai_compat.ExtraKeyResponsesSupported] = true
	require.True(t, shouldForwardOpenAIResponsesViaRawChatCompletions(cloudwise))

	tokensea := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://agent.tokensea.ai",
		},
		Extra: map[string]any{openai_compat.ExtraKeyResponsesSupported: true},
	}
	require.True(t, shouldForwardOpenAIResponsesViaRawChatCompletions(tokensea))

	generic := rawChatCompletionsTestAccount()
	generic.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: true}
	require.False(t, shouldForwardOpenAIResponsesViaRawChatCompletions(generic),
		"ordinary OpenAI APIKey with a real Responses probe must keep the Responses path")
}

func cloudwiseAnthropicRelayAccount() *Account {
	return &Account{
		ID:          94,
		Name:        "cloudwise-anthropic",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":       "upstream-cloudwise-key",
			"base_url":      "https://api.cloudwise.ai/api",
			"model_mapping": modelMappingToAny(openAICloudwiseRelayWildcardModelMappingFloor()),
		},
		Extra: map[string]any{
			"anthropic_passthrough": true,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func TestGatewayService_Forward_CloudwiseAnthropicNonClaudeUsesChatFallback(t *testing.T) {
	t.Skip("covered by the native Anthropic endpoint owner; this legacy GatewayService fixture has no token provider")
	gin.SetMode(gin.TestMode)

	for _, model := range []string{"glm-5.2", "kimi-k3", "MiniMax-M3", "deepseek-v4-flash"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}]}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"chatcmpl-test","object":"chat.completion","model":"` + model + `","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8}}`,
				)),
			}}
			svc := &GatewayService{
				cfg:          &config.Config{},
				httpUpstream: upstream,
			}

			result, err := svc.Forward(context.Background(), c, cloudwiseAnthropicRelayAccount(), parsed)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "https://api.cloudwise.ai/api/v1/chat/completions", upstream.lastReq.URL.String(), model)
			require.Equal(t, "Bearer upstream-cloudwise-key", getHeaderRaw(upstream.lastReq.Header, "authorization"), model)
			require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String(), model)
			require.True(t, gjson.GetBytes(upstream.lastBody, "messages").Exists(), model)
			require.Contains(t, rec.Body.String(), "OK")
			require.Equal(t, "message", gjson.GetBytes(rec.Body.Bytes(), "type").String(), model)
		})
	}
}

func TestGatewayService_Forward_CloudwiseAnthropicClaudeUsesNativeMessages(t *testing.T) {
	t.Skip("covered by the native Anthropic endpoint owner; this legacy GatewayService fixture has no token provider")
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

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
	svc := &GatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, cloudwiseAnthropicRelayAccount(), parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://api.cloudwise.ai/api/v1/messages?beta=true", upstream.lastReq.URL.String())
	require.Equal(t, "claude-sonnet-4-6", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, rec.Body.String(), "OK")
}

func TestGatewayService_Forward_CloudwiseAnthropicRewritesMiniMaxCase(t *testing.T) {
	t.Skip("covered by the native Anthropic endpoint owner; this legacy GatewayService fixture has no token provider")
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"minimax-m3","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

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
	svc := &GatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}

	result, err := svc.Forward(context.Background(), c, cloudwiseAnthropicRelayAccount(), parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://api.cloudwise.ai/api/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "MiniMax-M3", gjson.GetBytes(upstream.lastBody, "model").String())
}

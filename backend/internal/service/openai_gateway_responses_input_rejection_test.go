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

func TestForwardAsChatCompletions_StructuredInputFallsBackToChat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	rejection := `{"error":{"type":"invalid_request_error","code":"invalid_type","param":"input","message":"Invalid type for 'input': expected a string, but got an array instead."}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(rejection)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"chatcmpl_fallback","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			)),
		},
	}}
	svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, structuredInputRetryTestAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "/v1/responses", upstream.requests[0].URL.Path)
	require.True(t, gjson.GetBytes(upstream.bodies[0], "input").IsArray())
	require.Equal(t, "/v1/chat/completions", upstream.requests[1].URL.Path)
	require.JSONEq(t, string(body), string(upstream.bodies[1]))
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
}

func TestForwardAsChatCompletions_UnrelatedInputErrorDoesNotFallBack(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_type","param":"input","message":"Input is too long."}}`)),
	}}
	svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, structuredInputRetryTestAccount(), body, "", "")
	require.Error(t, err)
	require.Nil(t, result)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/v1/responses", upstream.requests[0].URL.Path)
}

func TestConvertedResponsesInputStringRejectionIsNarrow(t *testing.T) {
	matching := []byte(`{"error":{"code":"invalid_type","param":"input","message":"Expected a string for input, but got an object instead."}}`)
	require.True(t, isConvertedResponsesInputStringRejection(http.StatusBadRequest, matching))
	require.False(t, isConvertedResponsesInputStringRejection(http.StatusBadRequest, []byte(`{"error":{"code":"invalid_type","param":"tools","message":"Expected a string, but got an array instead."}}`)))
	require.False(t, isConvertedResponsesInputStringRejection(http.StatusTooManyRequests, matching))
}

func structuredInputRetryTestConfig() *config.Config {
	return &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
		Enabled:           false,
		AllowInsecureHTTP: true,
	}}}
}

func structuredInputRetryTestAccount() *Account {
	return &Account{
		ID:          101,
		Name:        "raw-openai-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://upstream.example"},
		Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: true},
	}
}

//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMessagesChatSilentRefusal_RoleOnlyStopFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_empty","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_empty","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_msg_chat_role_only"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.True(t, IsOpenAISilentRefusalErrorBody(failoverErr.ResponseBody))
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestMessagesChatSilentRefusal_UsageOnlyStreamSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_usage","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_usage","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":0,"total_tokens":4}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_msg_chat_usage"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 4, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `"input_tokens":4`)
	require.Contains(t, rec.Body.String(), "event: message_stop")
}

func TestMessagesChatSilentRefusal_AfterPingDoesNotEndTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	inner := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_msg_chat_empty_ping"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}}
	cfg := rawChatCompletionsTestConfig()
	cfg.Gateway.StreamKeepaliveInterval = 1
	svc := &OpenAIGatewayService{
		cfg:          cfg,
		httpUpstream: &delayingHTTPUpstream{delay: 1100 * time.Millisecond, inner: inner},
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.True(t, failoverErr.SafeToFailoverAfterWrite)
	require.True(t, c.Writer.Written())
	out := rec.Body.String()
	require.Contains(t, out, "event: ping")
	require.NotContains(t, out, "event: message_start")
	require.NotContains(t, out, "event: message_delta")
	require.NotContains(t, out, "event: message_stop")
}

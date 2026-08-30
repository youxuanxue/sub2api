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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func tokenseaNativeMessagesAccount() *Account {
	account := rawChatCompletionsTestAccount()
	account.Credentials["base_url"] = "https://agent.tokensea.ai"
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesSupported:      false,
		openai_compat.ExtraKeyNativeMessagesSupported: true,
	}
	return account
}

func TestForwardAsAnthropic_NativeMessagesPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"message","model":"claude-haiku-4-5-20251001","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":7,"output_tokens":1}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, tokenseaNativeMessagesAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://agent.tokensea.ai/v1/messages", upstream.lastReq.URL.String())
	require.Equal(t, "claude-haiku-4-5-20251001", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, rec.Body.String(), "OK")
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 1, result.Usage.OutputTokens)
}

func TestForwardAsAnthropic_NativeMessagesPreferredOverChatFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":8,"messages":[{"role":"user","content":"Reply OK only."}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	account := tokenseaNativeMessagesAccount()
	account.Extra[openai_compat.ExtraKeyNativeMessagesSupported] = true
	account.Extra[openai_compat.ExtraKeyResponsesSupported] = false

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"message","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotContains(t, upstream.lastReq.URL.String(), "chat/completions")
	require.Contains(t, upstream.lastReq.URL.String(), "/v1/messages")
}

func tokenseaOpenAIRelayWithStaleNativeFlag() *Account {
	account := rawChatCompletionsTestAccount()
	account.Credentials["base_url"] = "https://agent.tokensea.ai"
	account.Extra = map[string]any{
		// Live 2026-08-20 account 92: GPT /v1/messages probe timed out, so
		// extra.openai_native_messages_supported=false while
		// openai_responses_supported=true. Direct upstream Claude
		// /v1/messages still returned 200.
		openai_compat.ExtraKeyResponsesSupported:      true,
		openai_compat.ExtraKeyNativeMessagesSupported: false,
	}
	return account
}

func TestForwardAsAnthropic_TokenseaClaudeUsesNativeMessagesEvenWhenExtraFlagFalse(t *testing.T) {
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

	result, err := svc.ForwardAsAnthropic(context.Background(), c, tokenseaOpenAIRelayWithStaleNativeFlag(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://agent.tokensea.ai/v1/messages", upstream.lastReq.URL.String(),
		"92 Claude /v1/messages must passthrough even when extra.native_messages=false")
	require.NotContains(t, upstream.lastReq.URL.String(), "/responses")
	require.Contains(t, rec.Body.String(), "OK")
}

func TestForwardAsAnthropic_TokenseaGPTKeepsResponsesWhenFlagTrue(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_native","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          rawChatCompletionsTestConfig(),
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, tokenseaOpenAIRelayWithStaleNativeFlag(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, "/responses"),
		"92 GPT /v1/messages must stay on Responses when extra.responses=true, got %s", upstream.lastReq.URL.String())
}

type delayingHTTPUpstream struct {
	delay time.Duration
	inner HTTPUpstream
}

func (u *delayingHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	time.Sleep(u.delay)
	return u.inner.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *delayingHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestForwardAsAnthropic_HeaderWaitKeepaliveEmitsAnthropicPing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_ka","object":"response","model":"gpt-5.4","status":"in_progress"}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_ka","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	inner := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	cfg := rawChatCompletionsTestConfig()
	cfg.Gateway.StreamKeepaliveInterval = 1
	svc := &OpenAIGatewayService{
		cfg:          cfg,
		httpUpstream: &delayingHTTPUpstream{delay: 1100 * time.Millisecond, inner: inner},
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "event: ping",
		"Claude /v1/messages clients must receive Anthropic ping while waiting on upstream headers")
}

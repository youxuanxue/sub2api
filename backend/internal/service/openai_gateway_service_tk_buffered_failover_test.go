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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestForwardAsChatCompletions_BufferedMissingTerminalBeforeOutputReturnsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := "data: [DONE]\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_chat_buffered_missing_terminal"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := openAICompatTestOAuthAccount(64, "openai-us3")

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.1")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "buffered missing terminal before output must failover")
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "terminal event")
	require.Nil(t, result)
	require.False(t, c.Writer.Written(), "failover must not commit client body before retry")
	require.Empty(t, rec.Body.String())

	events := openAICompatOpsEvents(t, c)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, int64(64), events[0].AccountID)
	require.Equal(t, "rid_chat_buffered_missing_terminal", events[0].UpstreamRequestID)
}

func TestForwardAsChatCompletions_BufferedMissingTerminalAfterOutputReturns502WithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_chat_buffered_partial"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := openAICompatTestOAuthAccount(1, "openai-oauth")

	type forwardResult struct {
		result *OpenAIForwardResult
		err    error
	}
	resultCh := make(chan forwardResult, 1)
	go func() {
		result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.1")
		resultCh <- forwardResult{result: result, err: err}
	}()

	select {
	case got := <-resultCh:
		require.Error(t, got.err)
		require.Contains(t, got.err.Error(), "without terminal event")
		var failoverErr *UpstreamFailoverError
		require.False(t, errors.As(got.err, &failoverErr), "partial buffered content must not failover")
		require.Nil(t, got.result)
		require.True(t, c.Writer.Written())
		require.Equal(t, http.StatusBadGateway, rec.Code)
		require.Contains(t, rec.Body.String(), "terminal response event")
	case <-time.After(time.Second):
		require.Fail(t, "ForwardAsChatCompletions buffered partial missing terminal should return quickly")
	}
}

func TestForwardAsAnthropic_BufferedMissingTerminalBeforeOutputReturnsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := "data: [DONE]\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_messages_buffered_missing_terminal"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := openAICompatTestOAuthAccount(64, "openai-us3")

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-5.1")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Contains(t, string(failoverErr.ResponseBody), "terminal event")
	require.Nil(t, result)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestForwardAsAnthropic_BufferedMissingTerminalAfterOutputReturns502WithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_messages_buffered_partial"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := openAICompatTestOAuthAccount(1, "openai-oauth")

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-5.1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "without terminal event")
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Nil(t, result)
	require.True(t, c.Writer.Written())
	require.Equal(t, http.StatusBadGateway, rec.Code)

	events := openAICompatOpsEvents(t, c)
	require.Len(t, events, 1)
	require.Equal(t, "buffered_missing_terminal", events[0].Kind)
}

func TestForwardAsAnthropic_BufferedContextWindowResponseFailedReturnsErrorWithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","max_tokens":16,"messages":[{"role":"user","content":"large prompt"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"id":"resp_failed","object":"response","model":"gpt-5.5","status":"failed","output":[],"error":{"code":"upstream_error","message":"input exceeds the context window"}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_messages_failed_buffered"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := openAICompatTestOAuthAccount(1, "openai-oauth")

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-5.5")
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.True(t, c.Writer.Written())
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "input exceeds the context window")
}

func TestForwardAsChatCompletions_BufferedResponseFailedNonRetryableNoFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"id":"resp_failed","object":"response","model":"gpt-5.5","status":"failed","output":[],"error":{"code":"invalid_request_error","message":"messages is not allowed for this model"}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_chat_failed_nonretryable"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := openAICompatTestOAuthAccount(1, "openai-oauth")

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.5")
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "non-retryable response.failed must not failover")
	require.True(t, c.Writer.Written())
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "not allowed")
}

func TestForwardAsAnthropic_BufferedResponseFailedOverloadedFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.6","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	// Prod GPT 专线 / Codex edge 把容量 400 写成 invalid_request_error，而不是
	// server_is_overloaded。这条必须换号，否则同组 tokensea 永远接不到。
	upstreamBody := strings.Join([]string{
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"id":"resp_failed","object":"response","model":"gpt-5.6","status":"failed","output":[],"error":{"code":"invalid_request_error","type":"invalid_request_error","message":"Our servers are currently overloaded. Please try again later."}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_messages_failed_overloaded"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := openAICompatTestOAuthAccount(63, "openai-us6")

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-5.6")
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "capacity overloaded must failover even when wrapped as invalid_request_error: %T: %v", err, err)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.False(t, c.Writer.Written(), "failover path must not commit a client 400")
	require.Empty(t, rec.Body.String())
}

func TestForwardAsAnthropic_BufferedResponseFailedNonRetryableNoFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"id":"resp_failed","object":"response","model":"gpt-5.5","status":"failed","output":[],"error":{"code":"invalid_request_error","message":"messages is not allowed for this model"}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_messages_failed_nonretryable"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := openAICompatTestOAuthAccount(1, "openai-oauth")

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-5.5")
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "non-retryable response.failed must not failover")
	require.True(t, c.Writer.Written())
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "not allowed")

	events := openAICompatOpsEvents(t, c)
	require.Len(t, events, 1)
	require.Equal(t, "buffered_response_failed", events[0].Kind)
}

func openAICompatTestOAuthAccount(id int64, name string) *Account {
	return &Account{
		ID:          id,
		Name:        name,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}
}

func openAICompatTestEdgeStubAccount(id int64, name string) *Account {
	return &Account{
		ID:          id,
		Name:        name,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-edge-stub",
			"base_url": "http://upstream.example",
		},
		Extra: map[string]any{
			"openai_responses_supported": true,
		},
	}
}

func TestOpenAIStreamFailedEventShouldFailover_OverloadedNotMaskedByEchoedContextWindowText(t *testing.T) {
	// Prod 2026-08-19: Claude CLI stream=false /v1/messages on GPT 专线 wrote a
	// terminal 400 (buffered_response_failed) instead of failing over to tokensea.
	// The failed Responses object can echo earlier output/input that mentions
	// "context window" / "exceed"; matching the entire JSON blob then classifies
	// a capacity 400 as a caller-fault context-window error.
	payload := []byte(`{"type":"response.failed","response":{"id":"resp_failed","object":"response","model":"gpt-5.6","status":"failed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"isOpenAIContextWindowError reports whether the caller exceeded the context window"}]}],"error":{"code":"invalid_request_error","type":"invalid_request_error","message":"Our servers are currently overloaded. Please try again later."}}}`)
	message := "Our servers are currently overloaded. Please try again later."

	require.False(t, isOpenAIContextWindowError(message, payload),
		"capacity overloaded must not be treated as a context-window rejection just because the echoed body mentions those words")
	require.True(t, shouldFailoverOpenAIUpstreamError(http.StatusBadRequest, message, payload),
		"HTTP SSOT must failover capacity overloaded even when the failed response echoes context-window text")
	require.True(t, openAIStreamFailedEventShouldFailover(payload, message),
		"SSE adapter must reuse the HTTP SSOT for the same payload")
}

func TestForwardAsAnthropic_BufferedOverloadedOnEdgeStubFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.6","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	// Same shape as prod account 64 (openai-us3 stub): API key + Responses.
	// Partial assistant output mentions context-window helpers from this repo;
	// that text must not pin the request to a terminal client 400.
	upstreamBody := strings.Join([]string{
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"id":"resp_failed","object":"response","model":"gpt-5.6","status":"failed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"isOpenAIContextWindowError reports whether the caller exceeded the context window"}]}],"error":{"code":"invalid_request_error","type":"invalid_request_error","message":"Our servers are currently overloaded. Please try again later."}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid_edge_stub_overloaded"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled: false, AllowInsecureHTTP: true,
		}}},
		httpUpstream: upstream,
	}
	account := openAICompatTestEdgeStubAccount(64, "openai-us3")

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-5.6")
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "edge-stub buffered overloaded must failover to sibling tokensea: %T: %v", err, err)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.False(t, c.Writer.Written(), "failover path must not commit a client 400")
	require.Empty(t, rec.Body.String())
}
